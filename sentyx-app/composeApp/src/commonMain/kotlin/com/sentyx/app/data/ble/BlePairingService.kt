package com.sentyx.app.data.ble

import com.juul.kable.Characteristic
import com.juul.kable.Peripheral
import com.juul.kable.PlatformAdvertisement
import com.juul.kable.Scanner
import com.juul.kable.WriteType
import com.juul.kable.characteristicOf
import com.sentyx.app.core.storage.KeyValueStore
import com.sentyx.app.core.storage.StorageKeys
import com.sentyx.app.data.api.SentyxApi
import com.sentyx.app.di.AppConfig
import com.sentyx.app.domain.model.ConnectionTestStep
import com.sentyx.app.domain.model.DiscoveredDevice
import com.sentyx.app.domain.model.FirmwareUpdate
import com.sentyx.app.domain.model.HardwareRequirement
import com.sentyx.app.domain.model.OnboardingPermission
import com.sentyx.app.domain.model.ScanState
import com.sentyx.app.domain.model.WifiNetwork
import com.sentyx.app.domain.repository.PairingService
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.channelFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.encodeToString
import kotlin.coroutines.cancellation.CancellationException as KCancellationException
import kotlin.uuid.ExperimentalUuidApi
import kotlin.uuid.Uuid

/**
 * Real [PairingService] over BLE (Kable) + the backend register call.
 *
 * Session shape: [scan] discovers advertisements of [SentyxGatt.SERVICE_UUID];
 * [beginPairing] connects the chosen peripheral, reads DeviceInfo, and holds a
 * single Status-notify subscription for the rest of the session (feeding
 * [Session.statuses]). Every control/config write awaits the matching Status
 * state on that shared flow, subscribing before it writes to avoid missing a
 * fast notify, and throws with the device's `detail` on a rejection.
 *
 * MTU note: Kable does not expose the negotiated ATT MTU portably in
 * commonMain, so Config framing uses the conservative [SentyxGatt.DEFAULT_MAX_FRAME]
 * (20-byte) frames. Correctness is unaffected — only the frame count.
 */
@OptIn(ExperimentalUuidApi::class)
class BlePairingService(
    private val scope: CoroutineScope,
    private val api: SentyxApi,
    private val config: AppConfig,
    private val store: KeyValueStore,
) : PairingService {

    /** The id of the peripheral in the current session, if connected. */
    val pairedDeviceId: String?
        get() = session?.deviceId

    // Copied from the demo service: these are static onboarding UI content, not
    // device-derived, so the real service presents the same checklist/permissions.
    override val hardwareChecklist: List<HardwareRequirement> = listOf(
        HardwareRequirement("🍓", "Sentyx Pi installed", "Connected to your Tesla USB port and powered on"),
        HardwareRequirement("📶", "Bluetooth on", "For first-time pairing and nearby control"),
        HardwareRequirement("🔑", "Your Sentyx account", "To link the Pi to the cloud backend"),
    )

    override val permissions: List<OnboardingPermission> = listOf(
        OnboardingPermission("ble", "ᔨ", "Bluetooth", "Discover and pair the Pi", true),
        OnboardingPermission("net", "🌐", "Local network", "Talk to the Pi over Wi-Fi", true),
        OnboardingPermission("notif", "🔔", "Notifications", "Alert you about events", false),
    )

    private val serviceUuid = Uuid.parse(SentyxGatt.SERVICE_UUID)
    private val deviceInfoUuid = Uuid.parse(SentyxGatt.DEVICE_INFO_UUID)
    private val controlUuid = Uuid.parse(SentyxGatt.CONTROL_UUID)
    private val statusUuid = Uuid.parse(SentyxGatt.STATUS_UUID)
    private val configUuid = Uuid.parse(SentyxGatt.CONFIG_UUID)

    private val scanner: Scanner<PlatformAdvertisement> by lazy {
        Scanner {
            filters {
                match { services = listOf(serviceUuid) }
            }
        }
    }

    /** Advertisements seen during the most recent scan, keyed by DiscoveredDevice.id. */
    private val discovered = mutableMapOf<String, PlatformAdvertisement>()

    private var session: Session? = null

    // ---- Scan ---------------------------------------------------------------

    override fun scan(): Flow<ScanState> = channelFlow {
        send(ScanState.Scanning)
        discovered.clear()
        val found = LinkedHashMap<String, DiscoveredDevice>()
        try {
            withTimeoutOrNull(SCAN_MS) {
                scanner.advertisements.collect { adv ->
                    val id = adv.identifier.toString()
                    discovered[id] = adv
                    val name = adv.name ?: adv.peripheralName ?: "Sentyx Pi"
                    found[id] = DiscoveredDevice(
                        id = id,
                        name = name,
                        subtitle = "Sentyx Pi · signal ${adv.rssi} dBm",
                    )
                    send(ScanState.Found(found.values.toList()))
                }
            }
        } catch (e: KCancellationException) {
            throw e
        } catch (e: Throwable) {
            // A scan failure (e.g. adapter off) surfaces as "no devices" rather
            // than crashing the onboarding flow.
        }
        if (found.isEmpty()) send(ScanState.NoneFound)
    }

    // ---- Pairing ------------------------------------------------------------

    override suspend fun beginPairing(device: DiscoveredDevice) {
        cleanup()
        val advertisement = discovered[device.id]
            ?: fail("That device is no longer nearby. Scan again.")

        // Bound the whole connect → (re)bond → authenticate dance.
        val status = withTimeoutOrNull(PAIR_MS) {
            var s = connectAndSetup(advertisement, device)
            session = s

            // The first write to an encrypt-write characteristic triggers Just
            // Works bonding; many Android stacks (notably Samsung) drop the GATT
            // link the instant bonding completes, so the begin_pair write fails
            // with NotConnectedException. Tolerate it: reconnect (the bond now
            // exists) and retry the encrypted write once.
            try {
                awaitStatus(s, OP_MS, ControlOp.beginPair()) {
                    it.state == "authenticated" || it.ok == false
                }
            } catch (e: KCancellationException) {
                throw e
            } catch (e: Throwable) {
                log("begin_pair: first attempt failed (${e.message}); reconnecting after bond")
                cleanup()
                s = connectAndSetup(advertisement, device)
                session = s
                awaitStatus(s, OP_MS, ControlOp.beginPair()) {
                    it.state == "authenticated" || it.ok == false
                }
            }
        }
        if (status == null) {
            log("beginPairing: overall ${PAIR_MS}ms budget exceeded")
            cleanup()
            fail("Pairing took too long. Move closer to the device and retry.")
        }
        if (status.state != "authenticated") {
            fail(status.detail ?: "The device rejected pairing.")
        }
    }

    /**
     * Connect [advertisement], read its DeviceInfo, and open the single
     * Status-notify subscription that feeds [Session.statuses] for the session.
     * Throws a [PairingException] on failure; the returned session is not yet
     * stored in [session].
     */
    private suspend fun connectAndSetup(
        advertisement: PlatformAdvertisement,
        device: DiscoveredDevice,
    ): Session {
        val peripheral = Peripheral(advertisement)
        log("connect: ${device.name} (${device.id})")
        try {
            withTimeout(CONNECT_MS) { peripheral.connect() }
            log("connect: ok")
        } catch (e: TimeoutCancellationException) {
            log("connect: timeout after ${CONNECT_MS}ms")
            runCatching { peripheral.close() }
            fail("Couldn't connect to ${device.name} in time. Move closer and retry.")
        } catch (e: KCancellationException) {
            throw e
        } catch (e: Throwable) {
            log("connect: failed: ${e.message}")
            runCatching { peripheral.close() }
            fail("Couldn't connect to ${device.name}: ${e.message ?: "connection failed"}", e)
        }

        val controlChar = characteristicOf(serviceUuid, controlUuid)
        val statusChar = characteristicOf(serviceUuid, statusUuid)
        val configChar = characteristicOf(serviceUuid, configUuid)
        val deviceInfoChar = characteristicOf(serviceUuid, deviceInfoUuid)

        val deviceId = try {
            val bytes = withTimeout(OP_MS) { peripheral.read(deviceInfoChar) }
            SentyxGatt.json.decodeFromString<DeviceInfoDto>(bytes.decodeToString()).deviceId
        } catch (e: KCancellationException) {
            throw e
        } catch (e: Throwable) {
            runCatching { peripheral.close() }
            fail("Couldn't read device info from ${device.name}: ${e.message ?: "read failed"}", e)
        }

        // Hold one Status-notify subscription for the whole session. The collect
        // is wrapped so a dropped connection (e.g. the bonding disconnect, or a
        // CCCD write colliding with bonding) ends this job without crashing the
        // app; its death is recorded in [Session.notifyDead] so awaiting ops can
        // fail fast (see [awaitStatusVia]) instead of timing out on a dead flow.
        val statuses = MutableSharedFlow<StatusDto>(replay = 0, extraBufferCapacity = 32)
        val notifyDead = CompletableDeferred<Throwable>()
        val sessionScope = CoroutineScope(scope.coroutineContext + SupervisorJob(scope.coroutineContext[Job]))
        val observeJob = sessionScope.launch {
            log("status subscribe: start")
            try {
                peripheral.observe(statusChar).collect { bytes ->
                    val status = runCatching {
                        SentyxGatt.json.decodeFromString<StatusDto>(bytes.decodeToString())
                    }.getOrNull()
                    if (status != null) {
                        log("status notify: state=${status.state} ok=${status.ok} detail=${status.detail}")
                        statuses.emit(status)
                    } else {
                        log("status notify: unparseable (${bytes.size} bytes)")
                    }
                }
                log("status subscribe: flow completed")
                notifyDead.complete(PairingException("Status notifications ended."))
            } catch (e: KCancellationException) {
                throw e
            } catch (e: Throwable) {
                log("status subscribe: died: ${e.message}")
                notifyDead.complete(e)
            }
        }

        return Session(
            peripheral = peripheral,
            deviceId = deviceId,
            statuses = statuses,
            notifyDead = notifyDead,
            observeJob = observeJob,
            controlChar = controlChar,
            statusChar = statusChar,
            configChar = configChar,
        )
    }

    override suspend fun configure(
        deviceName: String,
        vehicleModel: String,
        nickname: String?,
        timezone: String,
    ) {
        val s = requireSession()

        val token = try {
            api.registerDevice(s.deviceId, deviceName)
        } catch (e: KCancellationException) {
            throw e
        } catch (e: Throwable) {
            fail("Couldn't register the device with Sentyx: ${e.message ?: "network error"}", e)
        }

        val payload = SentyxGatt.json.encodeToString(
            ConfigDto(
                serverUrl = config.serverBaseUrl,
                token = token,
                deviceName = deviceName,
                timezone = timezone.ifBlank { null },
                vehicleModel = vehicleModel.ifBlank { null },
                nickname = nickname?.ifBlank { null },
            ),
        ).encodeToByteArray()

        val frames = SentyxGatt.chunkConfig(payload, SentyxGatt.maxFrameFor(null))

        val status = coroutineScope {
            val awaiting = async(start = CoroutineStart.UNDISPATCHED) {
                awaitStatusVia(s, OP_MS) { it.state == "config_saved" || it.ok == false }
            }
            try {
                log("write config: ${frames.size} frames, ${payload.size} bytes")
                for (frame in frames) {
                    s.peripheral.write(s.configChar, frame, WriteType.WithResponse)
                }
                log("write config: done")
            } catch (e: KCancellationException) {
                throw e
            } catch (e: Throwable) {
                awaiting.cancel()
                fail("Couldn't send configuration to the device: ${e.message ?: "write failed"}", e)
            }
            awaiting.await()
        }
        if (status.state != "config_saved") {
            fail(status.detail ?: "The device rejected the configuration.")
        }

        // Remember which device was onboarded so the home screen can poll its
        // real status after pairing (RealDeviceRepository reads these keys).
        store.putString(StorageKeys.DEVICE_ID, s.deviceId)
        store.putString(StorageKeys.DEVICE_NAME, deviceName)
    }

    // Wi-Fi provisioning is out of scope for this groundwork; the UI's "Skip"
    // path covers it. The Pi is expected to reach the backend over its own
    // (LTE/hotspot) connectivity.
    override suspend fun availableNetworks(): List<WifiNetwork> = emptyList()

    override suspend fun connectWifi(ssid: String, password: String?) {
        // no-op
    }

    override suspend fun runConnectionTest(): List<ConnectionTestStep> {
        val s = requireSession()
        val steps = mutableListOf<ConnectionTestStep>()
        // Individual "test_step" statuses are transient and only trustworthy
        // from notifies (a poll READ could observe the same step twice, so
        // polled test_steps are ignored for the step list). Completion, though,
        // rides the notify+poll combo: the terminal state ("config_saved" or
        // "locked") is durable, so polling alone is enough to finish the test
        // even with zero working notifies.
        val terminal = awaitStatus(
            s,
            TEST_MS,
            ControlOp.test(),
            onStatus = { status, source ->
                if (status.state == "test_step" && source == "notify") {
                    steps += ConnectionTestStep(
                        title = testStepTitle(status.step),
                        subtitle = status.detail ?: "",
                        passed = status.ok == true,
                    )
                }
            },
        ) { it.state == "config_saved" || it.state == "locked" }
        if (terminal.state != "config_saved" || terminal.ok == false) {
            fail(terminal.detail ?: "The connection test failed.")
        }
        if (steps.isEmpty()) {
            // Step notifies were missed (dead subscription; polling completed the
            // test). Synthesize one generic passed step from the terminal result.
            steps += ConnectionTestStep(
                title = "Connection check",
                subtitle = terminal.detail ?: "Device reached the Sentyx backend",
                passed = true,
            )
        }
        return steps
    }

    override suspend fun requiredFirmwareUpdate(): FirmwareUpdate? = null

    override suspend fun completePairing() {
        val s = requireSession()
        try {
            awaitStatus(s, OP_MS, ControlOp.complete()) { it.state == "done" }
        } catch (e: TimeoutCancellationException) {
            // The agent reboots right after 'complete'; a missing final notify
            // or a dropped connection here is expected, not a failure.
        } catch (e: KCancellationException) {
            throw e
        } catch (e: Throwable) {
            // Tolerate the connection dropping as the device restarts.
        } finally {
            cleanup()
        }
    }

    // ---- Internals ----------------------------------------------------------

    /**
     * Wait (bounded by [timeoutMs]) for a Status matching [predicate], fed from
     * TWO sources into the same await logic:
     *
     *  1. the session's Status-notify subscription ([Session.statuses]), and
     *  2. a poll loop that READs the Status characteristic every [POLL_MS]
     *     (Status is also readable and returns the current state as the same
     *     DTO) — so the op completes even if notifies never arrive (e.g. the
     *     CCCD subscription died in the bonding collision).
     *
     * Fail-fast instead of dead-flow timeouts: a poll READ failure when the
     * notify subscription is already dead ([Session.notifyDead]) — or three
     * consecutive poll failures regardless — means the connection is gone, and
     * the op throws immediately with the underlying error. A genuine [timeoutMs]
     * expiry (device reachable but never reaching the state) throws a
     * [PairingException] with a friendly message.
     *
     * [onStatus] observes every status seen, tagged with its source
     * ("notify"|"read"), before the predicate is applied.
     */
    private suspend fun awaitStatusVia(
        s: Session,
        timeoutMs: Long,
        onStatus: ((StatusDto, String) -> Unit)? = null,
        predicate: (StatusDto) -> Boolean,
    ): StatusDto {
        // withTimeoutOrNull (not withTimeout+catch): returns null only for ITS
        // OWN expiry, so an enclosing timeout's cancellation still propagates
        // as cancellation instead of being converted into a PairingException.
        val outcome =
            withTimeoutOrNull(timeoutMs) {
                coroutineScope {
                    val result = CompletableDeferred<StatusDto>()

                    fun offer(status: StatusDto, source: String) {
                        onStatus?.invoke(status, source)
                        if (predicate(status)) result.complete(status)
                    }

                    // UNDISPATCHED: runs until the collect suspends, i.e. the
                    // SharedFlow subscription is registered before we return to
                    // the caller (which then writes) — no notify can slip past.
                    val notifyJob = launch(start = CoroutineStart.UNDISPATCHED) {
                        s.statuses.collect { offer(it, "notify") }
                    }
                    val pollJob = launch {
                        var failures = 0
                        while (true) {
                            delay(POLL_MS)
                            val bytes = try {
                                s.peripheral.read(s.statusChar).also { failures = 0 }
                            } catch (e: KCancellationException) {
                                throw e
                            } catch (e: Throwable) {
                                failures++
                                log("status read: failed #$failures: ${e.message} (notifyDead=${s.notifyDead.isCompleted})")
                                if (s.notifyDead.isCompleted || failures >= MAX_POLL_FAILURES) {
                                    result.completeExceptionally(
                                        PairingException(
                                            "Lost the connection to the device: ${e.message ?: "read failed"}",
                                            e,
                                        ),
                                    )
                                }
                                continue
                            }
                            val status = runCatching {
                                SentyxGatt.json.decodeFromString<StatusDto>(bytes.decodeToString())
                            }.getOrNull()
                            if (status != null) {
                                log("status read: state=${status.state} ok=${status.ok} detail=${status.detail}")
                                offer(status, "read")
                            } else {
                                log("status read: unparseable (${bytes.size} bytes)")
                            }
                        }
                    }
                    try {
                        result.await()
                    } finally {
                        notifyJob.cancel()
                        pollJob.cancel()
                    }
                }
            }
        if (outcome == null) {
            log("awaitStatus: timed out after ${timeoutMs}ms")
            fail("Timed out waiting for the device to respond.")
        }
        return outcome
    }

    /** Write [op] then await a Status matching [predicate] (notify or poll-read). */
    private suspend fun awaitStatus(
        s: Session,
        timeoutMs: Long,
        op: ControlOp,
        onStatus: ((StatusDto, String) -> Unit)? = null,
        predicate: (StatusDto) -> Boolean,
    ): StatusDto = coroutineScope {
        // Start awaiting (UNDISPATCHED registers the notify subscription and the
        // poll loop) before writing, so a fast notify can't slip past.
        val awaiting = async(start = CoroutineStart.UNDISPATCHED) {
            awaitStatusVia(s, timeoutMs, onStatus = onStatus, predicate = predicate)
        }
        try {
            writeControl(s, op)
        } catch (e: KCancellationException) {
            throw e
        } catch (e: Throwable) {
            awaiting.cancel()
            fail("Couldn't send command to the device: ${e.message ?: "write failed"}", e)
        }
        awaiting.await()
    }

    private suspend fun writeControl(s: Session, op: ControlOp) {
        log("write control: op=${op.op}")
        val bytes = SentyxGatt.json.encodeToString(op).encodeToByteArray()
        s.peripheral.write(s.controlChar, bytes, WriteType.WithResponse)
        log("write control: op=${op.op} done")
    }

    private fun requireSession(): Session =
        session ?: fail("Not connected to a device. Start pairing again.")

    private fun cleanup() {
        val s = session ?: return
        session = null
        log("cleanup: closing session for ${s.deviceId}")
        s.observeJob.cancel()
        scope.launch {
            runCatching { s.peripheral.disconnect() }
            runCatching { s.peripheral.close() }
        }
    }

    private fun fail(message: String, cause: Throwable? = null): Nothing =
        throw PairingException(message, cause)

    private fun testStepTitle(step: String?): String = when (step) {
        "healthz" -> "Server reachable"
        "auth" -> "Backend authenticated"
        null -> "Connection check"
        else -> step
    }

    private class Session(
        val peripheral: Peripheral,
        val deviceId: String,
        val statuses: MutableSharedFlow<StatusDto>,
        /** Completed (with the cause) when the Status-notify collect job dies. */
        val notifyDead: CompletableDeferred<Throwable>,
        val observeJob: Job,
        val controlChar: Characteristic,
        val statusChar: Characteristic,
        val configChar: Characteristic,
    )

    private companion object {
        const val SCAN_MS = 8_000L
        const val CONNECT_MS = 15_000L
        const val OP_MS = 15_000L
        const val TEST_MS = 60_000L

        /** Whole beginPairing budget: connect + possible rebond-reconnect + auth. */
        const val PAIR_MS = 30_000L

        /** Interval of the Status poll-READ fallback inside [awaitStatusVia]. */
        const val POLL_MS = 1_000L

        /** Consecutive poll-read failures (with a live notify sub) before failing the op. */
        const val MAX_POLL_FAILURES = 3

        /** Lightweight diagnostic logging (println reaches logcat via System.out). */
        fun log(message: String) = println("SentyxBLE: $message")
    }
}

/** Pairing failure carrying a message safe to show in the pairing UI. */
class PairingException(message: String, cause: Throwable? = null) :
    Exception(message, cause)

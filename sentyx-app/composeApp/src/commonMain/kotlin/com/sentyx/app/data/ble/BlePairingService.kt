package com.sentyx.app.data.ble

import com.juul.kable.PlatformAdvertisement
import com.juul.kable.Scanner
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
import kotlinx.datetime.Clock
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.channelFlow
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.encodeToString
import kotlin.coroutines.cancellation.CancellationException as KCancellationException
import kotlin.uuid.ExperimentalUuidApi
import kotlin.uuid.Uuid

/**
 * Real [PairingService] over BLE (Kable) + the backend register call.
 *
 * Session shape: [scan] discovers advertisements of [SentyxGatt.SERVICE_UUID];
 * [beginPairing] connects the chosen peripheral (via [PiBleSession]) and holds a
 * single Status-notify subscription for the rest of the session. Every
 * control/config write awaits the matching Status state on that shared flow,
 * subscribing before it writes to avoid missing a fast notify, and throws with
 * the device's `detail` on a rejection. Wi-Fi setup rides the same authenticated
 * session (the Wi-Fi characteristics; see [PiBleSession.wifiRequest]).
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

    private val scanner: Scanner<PlatformAdvertisement> by lazy {
        Scanner {
            filters {
                match { services = listOf(serviceUuid) }
            }
        }
    }

    /** Advertisements seen during the most recent scan, keyed by DiscoveredDevice.id. */
    private val discovered = mutableMapOf<String, PlatformAdvertisement>()

    private val bondedDevices = BondedDevices()

    private var session: PiBleSession? = null

    /** Set by [configure], committed to [store] by [completePairing] — see configure. */
    private var pendingDeviceId: String? = null
    private var pendingDeviceName: String? = null

    // ---- Scan ---------------------------------------------------------------

    override fun scan(): Flow<ScanState> = channelFlow {
        send(ScanState.Scanning)
        discovered.clear()
        val found = LinkedHashMap<String, DiscoveredDevice>()

        // Seed from the bond table first, so a previously bonded Pi is offered
        // even when the scan below fails to match its advertisement (see
        // [BondedDevices] for why that happens). Without this the user saw an
        // empty list whose only escape was forgetting the device in system
        // Bluetooth settings.
        val bondedIds = mutableSetOf<String>()
        for (peripheral in bondedDevices.sentyxPeripherals()) {
            bondedIds += peripheral.identifier
            found[peripheral.identifier] = DiscoveredDevice(
                id = peripheral.identifier,
                name = peripheral.name,
                subtitle = "${shortId(peripheral.identifier)} · already paired with this phone",
                paired = true,
            )
        }
        if (found.isNotEmpty()) send(ScanState.Found(found.values.toList()))

        try {
            withTimeoutOrNull(SCAN_MS) {
                scanner.advertisements.collect { adv ->
                    val id = adv.identifier.toString()
                    discovered[id] = adv
                    val name = adv.name ?: adv.peripheralName ?: "Sentyx Pi"
                    // A bonded device that IS advertising replaces its seeded
                    // entry, keeping the live signal reading and the badge.
                    found[id] = DiscoveredDevice(
                        id = id,
                        name = name,
                        // The identifier is in the subtitle because every unit
                        // advertises the same LocalName: with two of them in
                        // range — a spare on the bench, a neighbour's — the list
                        // shows two identical rows whose order is not stable
                        // between scans, and picking one is a coin toss. Signal
                        // strength alone does not settle it either.
                        subtitle = "${shortId(id)} · signal ${adv.rssi} dBm",
                        paired = id in bondedIds,
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

    /**
     * Last block of a BLE identifier — enough to tell two units apart in a list
     * without turning a device row into a MAC address.
     */
    private fun shortId(identifier: String): String =
        identifier.takeLast(5).let { if (it.contains(':')) it else identifier.takeLast(4) }

    // ---- Pairing ------------------------------------------------------------

    override suspend fun beginPairing(device: DiscoveredDevice) {
        cleanup()
        val advertisement = discovered[device.id]
        if (advertisement == null && !device.paired) {
            fail("That device is no longer nearby. Scan again.")
        }

        // Prefer the live advertisement; a bonded device that has stopped
        // advertising is connected straight from the bond table instead. A fresh
        // peripheral is built per call, because the rebond retry below
        // reconnects after the first one has been closed.
        suspend fun openSession(): PiBleSession = if (advertisement != null) {
            PiBleSession.connect(scope, advertisement, device.name)
        } else {
            try {
                PiBleSession.connect(
                    scope,
                    bondedDevices.peripheralFor(device.id),
                    device.id,
                    device.name,
                )
            } catch (e: KCancellationException) {
                throw e
            } catch (e: Throwable) {
                // Unlike an advertisement, a bond says nothing about whether the
                // Pi is switched on — it outlives the device being unplugged. So
                // a failure here is nearly always "not powered / out of range",
                // which the underlying BLE text ("Disconnect detected") does not
                // convey to anyone.
                log("bonded connect failed: ${e.message}")
                fail(unreachableBonded(device.name), e)
            }
        }

        // A unit we can see advertising that then refuses the link is the
        // signature of a bond it no longer holds: reflashing wipes the unit's
        // bond store, its BLE address does not change, and nothing tells
        // Android. The phone offers keys the unit cannot answer and the unit
        // drops the link within a few hundred milliseconds ("Disconnect
        // detected"). The app created that bond and system Bluetooth settings
        // do not list it, so the user cannot clear it — drop it here and
        // connect again. Costs one extra attempt when the cause was something
        // else, which beats stranding every reflashed unit.
        suspend fun openSessionClearingStaleBond(): PiBleSession =
            if (advertisement == null) {
                openSession() // the bonded path reports its own failure
            } else {
                try {
                    openSession()
                } catch (e: KCancellationException) {
                    throw e
                } catch (e: Throwable) {
                    log("connect refused (${e.message}); clearing the bond and retrying")
                    if (!bondedDevices.removeBond(device.id)) throw e
                    delay(REBOND_SETTLE_MS)
                    openSession()
                }
            }

        // Bound the whole connect → (re)bond → authenticate dance.
        val status = withTimeoutOrNull(PAIR_MS) {
            var s = openSessionClearingStaleBond()
            session = s

            // The first write to an encrypt-write characteristic triggers Just
            // Works bonding; many Android stacks (notably Samsung) drop the GATT
            // link the instant bonding completes, so the begin_pair write fails
            // with NotConnectedException. Tolerate it: reconnect (the bond now
            // exists) and retry the encrypted write once.
            try {
                s.awaitStatus(OP_MS, ControlOp.beginPair()) {
                    it.state == "authenticated" || it.ok == false
                }
            } catch (e: KCancellationException) {
                throw e
            } catch (e: Throwable) {
                log("begin_pair: first attempt failed (${e.message}); reconnecting after bond")
                cleanup()
                s = openSession()
                session = s
                try {
                    s.awaitStatus(OP_MS, ControlOp.beginPair()) {
                        it.state == "authenticated" || it.ok == false
                    }
                } catch (second: KCancellationException) {
                    throw second
                } catch (second: Throwable) {
                    // Failing twice is the signature of a bond this phone kept
                    // and the unit no longer has — reflashing wipes the unit's
                    // side, and nothing tells Android. The bond was created by
                    // this app and is not listed in system Bluetooth settings,
                    // so the user cannot clear it: drop it here and pair again.
                    log("begin_pair: retry failed (${second.message}); clearing the bond and pairing again")
                    cleanup()
                    if (!bondedDevices.removeBond(device.id)) throw second
                    s = openSession()
                    session = s
                    s.awaitStatus(OP_MS, ControlOp.beginPair()) {
                        it.state == "authenticated" || it.ok == false
                    }
                }
            }
        }
        if (status == null) {
            log("beginPairing: overall ${PAIR_MS}ms budget exceeded")
            cleanup()
            // Same reasoning as the bonded connect failure above: for a bonded
            // device, point at power/range and offer the stale-bond escape
            // hatch rather than an unqualified "retry".
            if (advertisement == null) {
                fail(unreachableBonded(device.name))
            }
            fail("Pairing took too long. Move closer to the device and retry.")
        }
        if (status.state != "authenticated") {
            fail(status.detail ?: "The device rejected pairing.")
        }
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
                // The unit has no RTC; without this it runs the connection test
                // believing it is still its image build date, and TLS rejects
                // the server certificate as not yet valid.
                nowUnixMs = Clock.System.now().toEpochMilliseconds(),
            ),
        ).encodeToByteArray()

        val status = coroutineScope {
            val awaiting = async(start = CoroutineStart.UNDISPATCHED) {
                s.awaitStatusVia(OP_MS) { it.state == "config_saved" || it.ok == false }
            }
            try {
                s.writeFramed(s.configChar, payload)
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

        // Deliberately NOT stored here: config_saved is in-memory on the device —
        // it only persists after the connection test passes, and the device only
        // becomes provisioned at `complete`. Recording DEVICE_ID this early made
        // an onboarding that died between here and completePairing (seen in the
        // field: the app crashed on the Wi-Fi screen) leave the app in manage
        // mode against a device that never provisioned, where every begin_manage
        // is correctly rejected ("requires a provisioned device"). The keys are
        // stored in completePairing instead.
        pendingDeviceId = s.deviceId
        pendingDeviceName = deviceName
    }

    // Wi-Fi setup rides the already-authenticated pairing session (begin_pair
    // authenticated it), so these reuse [PiBleSession.wifiRequest] on the same
    // connection. A failed *scan* is still tolerated (an empty list degrades to
    // "no networks found"), but a failed *join* now throws — see connectWifi.
    override suspend fun availableNetworks(): List<WifiNetwork> {
        val s = session ?: return emptyList()
        return try {
            val json = s.wifiRequest(
                SentyxGatt.json.encodeToString(WifiCommandDto.scan()).encodeToByteArray(),
                WIFI_OP_MS,
            )
            val result = SentyxGatt.json.decodeFromString<WifiResultDto>(json)
            if (result.ok) result.networks.map { it.toWifiNetwork() } else emptyList()
        } catch (e: KCancellationException) {
            throw e
        } catch (e: Throwable) {
            log("availableNetworks: ${e.message}")
            emptyList()
        }
    }

    /**
     * Unlike the scan above, a failed join is NOT swallowed. Reporting it here
     * is the only way the user learns the password was wrong: the connection
     * test that follows reports "couldn't reach the server", which is true but
     * says nothing about the cause. The caller shows this message inline and
     * stays on the Wi-Fi screen, so a rejection no longer costs a retry of the
     * whole onboarding.
     */
    override suspend fun connectWifi(ssid: String, password: String?) {
        val s = session ?: fail("Not connected to your Sentyx Pi. Reconnect and try again.")
        val json = try {
            s.wifiRequest(
                SentyxGatt.json.encodeToString(
                    WifiCommandDto.connect(ssid, password?.ifBlank { null }),
                ).encodeToByteArray(),
                WIFI_CONNECT_MS,
            )
        } catch (e: KCancellationException) {
            throw e
        } catch (e: Throwable) {
            log("connectWifi: ${e.message}")
            fail("Lost the Bluetooth link while joining \"$ssid\". Try again.", e)
        }
        val result = try {
            SentyxGatt.json.decodeFromString<WifiResultDto>(json)
        } catch (e: KCancellationException) {
            throw e
        } catch (e: Throwable) {
            fail("The device sent an unreadable reply while joining \"$ssid\".", e)
        }
        if (!result.ok) fail(result.detail ?: "Couldn't join \"$ssid\".")
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
        val terminal = s.awaitStatus(
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
            s.awaitStatus(OP_MS, ControlOp.complete()) { it.state == "done" }
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
        // Only now is the device provisioned (config persisted by the passed
        // connection test, agent restarting into provisioned mode), so only now
        // does the app record it as onboarded (RealDeviceRepository reads these).
        pendingDeviceId?.let { store.putString(StorageKeys.DEVICE_ID, it) }
        pendingDeviceName?.let { store.putString(StorageKeys.DEVICE_NAME, it) }
    }

    // ---- Internals ----------------------------------------------------------

    private fun requireSession(): PiBleSession =
        session ?: fail("Not connected to a device. Start pairing again.")

    private fun cleanup() {
        val s = session ?: return
        session = null
        s.close(scope)
    }

    private fun fail(message: String, cause: Throwable? = null): Nothing =
        throw PairingException(message, cause)

    /** Copy for a bonded device that didn't answer — see [beginPairing]. */
    private fun unreachableBonded(name: String): String =
        "Couldn't reach $name. Check it's powered and in range. If it still " +
            "fails, forget the device in Bluetooth settings and pair again."

    private fun testStepTitle(step: String?): String = when (step) {
        // The device has no battery-backed clock, so it starts life weeks in
        // the past and every certificate looks "not yet valid" until NTP lands.
        // It waits for that before probing; naming the step keeps the pause on
        // the progress screen explicable rather than looking like a stall.
        "clock" -> "Clock synchronized"
        "healthz" -> "Server reachable"
        "auth" -> "Backend authenticated"
        null -> "Connection check"
        else -> step
    }

    private companion object {
        const val SCAN_MS = 8_000L
        const val OP_MS = 15_000L
        /**
         * Budget for the whole connection test. It covers more than two HTTP
         * probes: a unit that has never been online waits for its clock to be
         * set first, because without that every certificate looks invalid. The
         * device streams a step per stage, so a slow test shows progress rather
         * than an idle screen.
         */
        const val TEST_MS = 90_000L

        /** Status/scan/forget Wi-Fi op budget. */
        const val WIFI_OP_MS = 15_000L

        /** Join budget: the Pi's Wi-Fi can bounce for up to ~60s while connecting. */
        const val WIFI_CONNECT_MS = 70_000L

        /** Whole beginPairing budget: connect + possible rebond-reconnect + auth. */
        const val PAIR_MS = 30_000L

        /**
         * Pause between dropping a stale bond and reconnecting. Android removes
         * a bond asynchronously; connecting in the same breath can still be
         * served the keys that were just discarded.
         */
        const val REBOND_SETTLE_MS = 500L

        fun log(message: String) = println("SentyxBLE: $message")
    }
}

/** Pairing failure carrying a message safe to show in the pairing UI. */
class PairingException(message: String, cause: Throwable? = null) :
    Exception(message, cause)

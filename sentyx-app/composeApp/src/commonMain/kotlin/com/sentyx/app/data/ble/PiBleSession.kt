package com.sentyx.app.data.ble

import com.juul.kable.Characteristic
import com.juul.kable.Peripheral
import com.juul.kable.PlatformAdvertisement
import com.juul.kable.WriteType
import com.juul.kable.characteristicOf
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.withTimeoutOrNull
import kotlin.coroutines.cancellation.CancellationException as KCancellationException
import kotlin.uuid.ExperimentalUuidApi
import kotlin.uuid.Uuid

/**
 * A connected Sentyx Pi GATT link shared by every BLE feature (pairing, Wi-Fi
 * management). It owns the peripheral, the single Status-notify subscription
 * that feeds [statuses], and the low-level primitives every op is built from:
 * [awaitStatus]/[awaitStatusVia] (notify + poll-read await), [writeControl],
 * [writeFramed] (chunked writes), and [wifiRequest] (framed request/response on
 * the Wi-Fi characteristics). Higher-level orchestration — bonding retry, which
 * ops to run, error copy — stays in the callers.
 *
 * MTU note: Kable does not expose the negotiated ATT MTU portably in
 * commonMain, so framing uses the conservative [SentyxGatt.DEFAULT_MAX_FRAME]
 * (20-byte) frames. Correctness is unaffected — only the frame count.
 */
@OptIn(ExperimentalUuidApi::class)
internal class PiBleSession private constructor(
    val peripheral: Peripheral,
    val deviceId: String,
    /** Every Status DTO seen on the shared notify subscription. */
    val statuses: MutableSharedFlow<StatusDto>,
    /** Completed (with the cause) when the Status-notify collect job dies. */
    private val notifyDead: CompletableDeferred<Throwable>,
    private val observeJob: Job,
    val controlChar: Characteristic,
    val statusChar: Characteristic,
    val configChar: Characteristic,
    val wifiCmdChar: Characteristic,
    val wifiResultChar: Characteristic,
) {

    // ---- Status await -------------------------------------------------------

    /**
     * Wait (bounded by [timeoutMs]) for a Status matching [predicate], fed from
     * TWO sources into the same await logic:
     *
     *  1. the shared Status-notify subscription ([statuses]), and
     *  2. a poll loop that READs the Status characteristic every [POLL_MS]
     *     (Status is also readable and returns the current state as the same
     *     DTO) — so the op completes even if notifies never arrive (e.g. the
     *     CCCD subscription died in the bonding collision).
     *
     * Fail-fast instead of dead-flow timeouts: a poll READ failure when the
     * notify subscription is already dead ([notifyDead]) — or three consecutive
     * poll failures regardless — means the connection is gone, and the op throws
     * immediately with the underlying error. A genuine [timeoutMs] expiry
     * (device reachable but never reaching the state) throws a [BleGattException].
     *
     * [onStatus] observes every status seen, tagged with its source
     * ("notify"|"read"), before the predicate is applied.
     */
    suspend fun awaitStatusVia(
        timeoutMs: Long,
        onStatus: ((StatusDto, String) -> Unit)? = null,
        predicate: (StatusDto) -> Boolean,
    ): StatusDto {
        // withTimeoutOrNull (not withTimeout+catch): returns null only for ITS
        // OWN expiry, so an enclosing timeout's cancellation still propagates
        // as cancellation instead of being converted into a BleGattException.
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
                        statuses.collect { offer(it, "notify") }
                    }
                    val pollJob = launch {
                        var failures = 0
                        while (true) {
                            delay(POLL_MS)
                            val bytes = try {
                                peripheral.read(statusChar).also { failures = 0 }
                            } catch (e: KCancellationException) {
                                throw e
                            } catch (e: Throwable) {
                                failures++
                                log("status read: failed #$failures: ${e.message} (notifyDead=${notifyDead.isCompleted})")
                                if (notifyDead.isCompleted || failures >= MAX_POLL_FAILURES) {
                                    result.completeExceptionally(
                                        BleGattException(
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
            throw BleGattException("Timed out waiting for the device to respond.")
        }
        return outcome
    }

    /** Write [op] then await a Status matching [predicate] (notify or poll-read). */
    suspend fun awaitStatus(
        timeoutMs: Long,
        op: ControlOp,
        onStatus: ((StatusDto, String) -> Unit)? = null,
        predicate: (StatusDto) -> Boolean,
    ): StatusDto = coroutineScope {
        // Start awaiting (UNDISPATCHED registers the notify subscription and the
        // poll loop) before writing, so a fast notify can't slip past.
        val awaiting = async(start = CoroutineStart.UNDISPATCHED) {
            awaitStatusVia(timeoutMs, onStatus = onStatus, predicate = predicate)
        }
        try {
            writeControl(op)
        } catch (e: KCancellationException) {
            throw e
        } catch (e: Throwable) {
            awaiting.cancel()
            throw BleGattException("Couldn't send command to the device: ${e.message ?: "write failed"}", e)
        }
        awaiting.await()
    }

    // ---- Writes -------------------------------------------------------------

    suspend fun writeControl(op: ControlOp) {
        log("write control: op=${op.op}")
        val bytes = SentyxGatt.json.encodeToString(op).encodeToByteArray()
        peripheral.write(controlChar, bytes, WriteType.WithResponse)
        log("write control: op=${op.op} done")
    }

    /** Write [payload] to [char] as [SentyxGatt.chunkConfig] frames (with response). */
    suspend fun writeFramed(char: Characteristic, payload: ByteArray) {
        val frames = SentyxGatt.chunkConfig(payload, SentyxGatt.maxFrameFor(null))
        log("write framed: ${frames.size} frames, ${payload.size} bytes")
        for (frame in frames) {
            peripheral.write(char, frame, WriteType.WithResponse)
        }
        log("write framed: done")
    }

    // ---- Wi-Fi request/response --------------------------------------------

    /**
     * Run one Wi-Fi command: subscribe to [wifiResultChar], write [requestJson]
     * (framed) to [wifiCmdChar], and await exactly one reassembled framed
     * response, returned as a JSON string. Only one call may be in flight at a
     * time. Throws a [BleGattException] on write failure, a dropped notify, or a
     * [timeoutMs] expiry.
     */
    suspend fun wifiRequest(requestJson: ByteArray, timeoutMs: Long): String = coroutineScope {
        val response = CompletableDeferred<String>()

        // Subscribe BEFORE writing so a fast response can't slip past; the frames
        // of one response are reassembled with the shared grammar.
        val observe = launch(start = CoroutineStart.UNDISPATCHED) {
            val reassembler = SentyxGatt.FrameReassembler()
            try {
                peripheral.observe(wifiResultChar).collect { bytes ->
                    val complete = reassembler.accept(bytes)
                    if (complete != null) {
                        log("wifi result: ${complete.size} bytes")
                        response.complete(complete.decodeToString())
                    }
                }
                response.completeExceptionally(BleGattException("Wi-Fi notifications ended."))
            } catch (e: KCancellationException) {
                throw e
            } catch (e: Throwable) {
                response.completeExceptionally(
                    BleGattException("Lost the connection to the device: ${e.message ?: "notify failed"}", e),
                )
            }
        }

        try {
            writeFramed(wifiCmdChar, requestJson)
            withTimeoutOrNull(timeoutMs) { response.await() }
                ?: throw BleGattException("Timed out waiting for the device to respond.")
        } catch (e: KCancellationException) {
            throw e
        } catch (e: BleGattException) {
            throw e
        } catch (e: Throwable) {
            throw BleGattException("Couldn't send the Wi-Fi command to the device: ${e.message ?: "write failed"}", e)
        } finally {
            observe.cancel()
        }
    }

    // ---- Teardown -----------------------------------------------------------

    /** Cancel the notify subscription and disconnect/close on [scope]. */
    fun close(scope: CoroutineScope) {
        log("close: session for $deviceId")
        observeJob.cancel()
        scope.launch {
            runCatching { peripheral.disconnect() }
            runCatching { peripheral.close() }
        }
    }

    companion object {
        /** Connect budget for the peripheral link inside [connect]. */
        const val CONNECT_MS = 15_000L

        /** Read budget for the initial DeviceInfo read inside [connect]. */
        private const val READ_MS = 15_000L

        /** Interval of the Status poll-READ fallback inside [awaitStatusVia]. */
        private const val POLL_MS = 1_000L

        /** Consecutive poll-read failures (with a live notify sub) before failing the op. */
        private const val MAX_POLL_FAILURES = 3

        /** Lightweight diagnostic logging (println reaches logcat via System.out). */
        fun log(message: String) = println("SentyxBLE: $message")

        /**
         * Connect [advertisement], read its DeviceInfo, and open the single
         * Status-notify subscription that feeds [statuses] for the session's
         * lifetime. Throws a [BleGattException] on failure; the observe job is
         * launched on [parentScope]. [label] names the device in error copy.
         */
        suspend fun connect(
            parentScope: CoroutineScope,
            advertisement: PlatformAdvertisement,
            label: String,
        ): PiBleSession {
            val serviceUuid = Uuid.parse(SentyxGatt.SERVICE_UUID)
            val peripheral = Peripheral(advertisement)
            log("connect: $label (${advertisement.identifier})")
            try {
                withTimeout(CONNECT_MS) { peripheral.connect() }
                log("connect: ok")
            } catch (e: TimeoutCancellationException) {
                log("connect: timeout after ${CONNECT_MS}ms")
                runCatching { peripheral.close() }
                throw BleGattException("Couldn't connect to $label in time. Move closer and retry.")
            } catch (e: KCancellationException) {
                throw e
            } catch (e: Throwable) {
                log("connect: failed: ${e.message}")
                runCatching { peripheral.close() }
                throw BleGattException("Couldn't connect to $label: ${e.message ?: "connection failed"}", e)
            }

            val controlChar = characteristicOf(serviceUuid, Uuid.parse(SentyxGatt.CONTROL_UUID))
            val statusChar = characteristicOf(serviceUuid, Uuid.parse(SentyxGatt.STATUS_UUID))
            val configChar = characteristicOf(serviceUuid, Uuid.parse(SentyxGatt.CONFIG_UUID))
            val wifiCmdChar = characteristicOf(serviceUuid, Uuid.parse(SentyxGatt.WIFI_CMD_UUID))
            val wifiResultChar = characteristicOf(serviceUuid, Uuid.parse(SentyxGatt.WIFI_RESULT_UUID))
            val deviceInfoChar = characteristicOf(serviceUuid, Uuid.parse(SentyxGatt.DEVICE_INFO_UUID))

            val deviceId = try {
                val bytes = withTimeout(READ_MS) { peripheral.read(deviceInfoChar) }
                SentyxGatt.json.decodeFromString<DeviceInfoDto>(bytes.decodeToString()).deviceId
            } catch (e: KCancellationException) {
                throw e
            } catch (e: Throwable) {
                runCatching { peripheral.close() }
                throw BleGattException("Couldn't read device info from $label: ${e.message ?: "read failed"}", e)
            }

            // Hold one Status-notify subscription for the whole session. The
            // collect is wrapped so a dropped connection (e.g. the bonding
            // disconnect, or a CCCD write colliding with bonding) ends this job
            // without crashing the app; its death is recorded in [notifyDead] so
            // awaiting ops can fail fast (see [awaitStatusVia]) instead of timing
            // out on a dead flow.
            val statuses = MutableSharedFlow<StatusDto>(replay = 0, extraBufferCapacity = 32)
            val notifyDead = CompletableDeferred<Throwable>()
            val sessionScope = CoroutineScope(parentScope.coroutineContext + SupervisorJob(parentScope.coroutineContext[Job]))
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
                    notifyDead.complete(BleGattException("Status notifications ended."))
                } catch (e: KCancellationException) {
                    throw e
                } catch (e: Throwable) {
                    log("status subscribe: died: ${e.message}")
                    notifyDead.complete(e)
                }
            }

            return PiBleSession(
                peripheral = peripheral,
                deviceId = deviceId,
                statuses = statuses,
                notifyDead = notifyDead,
                observeJob = observeJob,
                controlChar = controlChar,
                statusChar = statusChar,
                configChar = configChar,
                wifiCmdChar = wifiCmdChar,
                wifiResultChar = wifiResultChar,
            )
        }
    }
}

/** A low-level BLE/GATT failure carrying a message safe to show in the UI. */
class BleGattException(message: String, cause: Throwable? = null) :
    Exception(message, cause)

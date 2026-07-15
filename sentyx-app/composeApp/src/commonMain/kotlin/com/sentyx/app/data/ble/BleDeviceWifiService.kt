package com.sentyx.app.data.ble

import com.juul.kable.PlatformAdvertisement
import com.juul.kable.Scanner
import com.sentyx.app.domain.model.WifiNetwork
import com.sentyx.app.domain.model.WifiStatus
import com.sentyx.app.domain.repository.DeviceWifiException
import com.sentyx.app.domain.repository.DeviceWifiService
import com.sentyx.app.domain.repository.WifiLinkState
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.firstOrNull
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.encodeToString
import kotlin.coroutines.cancellation.CancellationException as KCancellationException
import kotlin.uuid.ExperimentalUuidApi
import kotlin.uuid.Uuid

/**
 * Real [DeviceWifiService] over BLE (Kable), reusing [PiBleSession] for all GATT
 * plumbing. [connect] scans for the bonded Pi advertising [SentyxGatt.SERVICE_UUID],
 * connects, and authenticates for management with `begin_manage` (mirroring the
 * pairing service's bonding-collision reconnect+retry). The Wi-Fi ops then run
 * one-at-a-time over [PiBleSession.wifiRequest] on that connection. Failures
 * surface as [DeviceWifiException] with the device's own `detail` when present.
 */
@OptIn(ExperimentalUuidApi::class)
class BleDeviceWifiService(
    private val scope: CoroutineScope,
) : DeviceWifiService {

    private val _linkState = MutableStateFlow(WifiLinkState.Disconnected)
    override val linkState: StateFlow<WifiLinkState> = _linkState.asStateFlow()

    private val serviceUuid = Uuid.parse(SentyxGatt.SERVICE_UUID)

    private val scanner: Scanner<PlatformAdvertisement> by lazy {
        Scanner {
            filters {
                match { services = listOf(serviceUuid) }
            }
        }
    }

    private var session: PiBleSession? = null

    // ---- Link lifecycle -----------------------------------------------------

    override suspend fun connect() {
        release()
        _linkState.value = WifiLinkState.Connecting
        try {
            val advertisement = scanForDevice()
                ?: fail("Couldn't find your Sentyx Pi nearby. Make sure it's powered and in range.")

            // The device is bonded from onboarding, but the first encrypted write
            // can still drop the GATT link on some stacks (mirrors begin_pair);
            // reconnect once and retry the begin_manage write.
            val status = withTimeoutOrNull(CONNECT_TOTAL_MS) {
                var s = PiBleSession.connect(scope, advertisement, DEVICE_LABEL)
                session = s
                try {
                    s.awaitStatus(OP_MS, ControlOp.beginManage()) {
                        it.state == "authenticated" || it.ok == false
                    }
                } catch (e: KCancellationException) {
                    throw e
                } catch (e: Throwable) {
                    log("begin_manage: first attempt failed (${e.message}); reconnecting after bond")
                    releaseSession()
                    s = PiBleSession.connect(scope, advertisement, DEVICE_LABEL)
                    session = s
                    s.awaitStatus(OP_MS, ControlOp.beginManage()) {
                        it.state == "authenticated" || it.ok == false
                    }
                }
            }
            if (status == null) {
                log("connect: overall ${CONNECT_TOTAL_MS}ms budget exceeded")
                fail("Connecting to your Sentyx Pi took too long. Move closer and retry.")
            }
            if (status.state != "authenticated") {
                fail(status.detail ?: "The device wouldn't accept the connection.")
            }
            _linkState.value = WifiLinkState.Connected
        } catch (e: KCancellationException) {
            throw e
        } catch (e: Throwable) {
            releaseSession()
            _linkState.value = WifiLinkState.Disconnected
            if (e is DeviceWifiException) throw e
            fail(e.message ?: "Couldn't connect to your Sentyx Pi.", e)
        }
    }

    override fun release() {
        releaseSession()
        _linkState.value = WifiLinkState.Disconnected
    }

    // ---- Wi-Fi ops ----------------------------------------------------------

    override suspend fun status(): WifiStatus {
        val result = request(WifiCommandDto.status(), OP_MS)
        if (!result.ok) fail(result.detail ?: "Couldn't read the Wi-Fi status.")
        return result.toWifiStatus()
    }

    override suspend fun scan(): List<WifiNetwork> {
        val result = request(WifiCommandDto.scan(), OP_MS)
        if (!result.ok) fail(result.detail ?: "Couldn't scan for Wi-Fi networks.")
        return result.networks.map { it.toWifiNetwork() }
    }

    override suspend fun connectWifi(ssid: String, password: String?) {
        val result = request(WifiCommandDto.connect(ssid, password?.ifBlank { null }), CONNECT_WIFI_MS)
        if (!result.ok) fail(result.detail ?: "Couldn't join \"$ssid\".")
    }

    override suspend fun forget(ssid: String) {
        val result = request(WifiCommandDto.forget(ssid), OP_MS)
        if (!result.ok) fail(result.detail ?: "Couldn't forget \"$ssid\".")
    }

    // ---- Internals ----------------------------------------------------------

    private suspend fun request(command: WifiCommandDto, timeoutMs: Long): WifiResultDto {
        val s = session ?: fail("Not connected to your Sentyx Pi. Reconnect and try again.")
        val json = try {
            s.wifiRequest(SentyxGatt.json.encodeToString(command).encodeToByteArray(), timeoutMs)
        } catch (e: KCancellationException) {
            throw e
        } catch (e: BleGattException) {
            // A dropped link ends the settings session; reflect it so the UI can
            // offer to reconnect.
            _linkState.value = WifiLinkState.Disconnected
            fail(e.message ?: "Lost the connection to your Sentyx Pi.", e)
        } catch (e: Throwable) {
            fail(e.message ?: "The Wi-Fi command failed.", e)
        }
        return try {
            SentyxGatt.json.decodeFromString<WifiResultDto>(json)
        } catch (e: Throwable) {
            fail("The device sent an unreadable Wi-Fi response.", e)
        }
    }

    /** Scan for the first Sentyx advertisement, or null if none appears in time. */
    private suspend fun scanForDevice(): PlatformAdvertisement? =
        withTimeoutOrNull(SCAN_MS) {
            try {
                scanner.advertisements.firstOrNull()
            } catch (e: KCancellationException) {
                throw e
            } catch (e: Throwable) {
                log("scan: failed: ${e.message}")
                null
            }
        }

    private fun releaseSession() {
        session?.close(scope)
        session = null
    }

    private fun fail(message: String, cause: Throwable? = null): Nothing =
        throw DeviceWifiException(message, cause)

    private companion object {
        const val DEVICE_LABEL = "your Sentyx Pi"
        const val SCAN_MS = 8_000L
        const val OP_MS = 15_000L

        /** Join budget: the Pi's Wi-Fi can bounce for up to ~60s while connecting. */
        const val CONNECT_WIFI_MS = 70_000L

        /** Whole connect budget: connect + possible rebond-reconnect + authenticate. */
        const val CONNECT_TOTAL_MS = 30_000L

        fun log(message: String) = println("SentyxBLE: $message")
    }
}

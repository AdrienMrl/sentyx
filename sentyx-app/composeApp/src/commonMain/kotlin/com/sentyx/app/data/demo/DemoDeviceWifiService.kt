package com.sentyx.app.data.demo

import com.sentyx.app.domain.model.CurrentWifi
import com.sentyx.app.domain.model.SavedWifiNetwork
import com.sentyx.app.domain.model.WifiNetwork
import com.sentyx.app.domain.model.WifiStatus
import com.sentyx.app.domain.repository.DeviceWifiException
import com.sentyx.app.domain.repository.DeviceWifiService
import com.sentyx.app.domain.repository.WifiLinkState
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/**
 * Demo [DeviceWifiService]. Mirrors the real BLE session with plausible fake
 * data and realistic delays: connecting takes a beat, the Pi starts on "Garage
 * 5G" with two other saved networks, scans surface a few nearby SSIDs, and
 * connect/forget mutate the in-memory state so the settings screen reacts.
 */
class DemoDeviceWifiService : DeviceWifiService {

    private val _linkState = MutableStateFlow(WifiLinkState.Disconnected)
    override val linkState: StateFlow<WifiLinkState> = _linkState.asStateFlow()

    // Mutable in-memory state so connect/forget change what status() returns.
    private var current: CurrentWifi? = CurrentWifi("Garage 5G", signal = 88)
    private val saved = mutableListOf(
        SavedWifiNetwork("Garage 5G", active = true, autoconnect = true),
        SavedWifiNetwork("Home", active = false, autoconnect = true),
        SavedWifiNetwork("Driveway_2.4", active = false, autoconnect = false),
    )

    override suspend fun connect() {
        _linkState.value = WifiLinkState.Connecting
        delay(1400)
        _linkState.value = WifiLinkState.Connected
    }

    override fun release() {
        _linkState.value = WifiLinkState.Disconnected
    }

    override suspend fun status(): WifiStatus {
        delay(500)
        return WifiStatus(current = current, saved = saved.toList())
    }

    override suspend fun scan(): List<WifiNetwork> {
        delay(1600)
        return listOf(
            WifiNetwork("Garage 5G", "Saved · Secured · strong", requiresPassword = true),
            WifiNetwork("Home", "Saved · Secured · good", requiresPassword = true),
            WifiNetwork("Tesla Guest", "Open · good", requiresPassword = false),
            WifiNetwork("Neighbor_2.4", "Secured · weak", requiresPassword = true),
        )
    }

    override suspend fun connectWifi(ssid: String, password: String?) {
        // The Pi's Wi-Fi bounces while joining; BLE stays up (~5s here).
        delay(5000)
        if (password != null && password.length < 8) {
            throw DeviceWifiException("Couldn't join \"$ssid\": the password looks too short.")
        }
        current = CurrentWifi(ssid, signal = 80)
        val reactivated = saved.map { it.copy(active = it.ssid == ssid) }
        saved.clear()
        saved.addAll(reactivated)
        if (saved.none { it.ssid == ssid }) {
            saved.add(SavedWifiNetwork(ssid, active = true, autoconnect = true))
        }
    }

    override suspend fun forget(ssid: String) {
        delay(800)
        val wasActive = current?.ssid == ssid
        saved.removeAll { it.ssid == ssid }
        if (wasActive) current = null
    }
}

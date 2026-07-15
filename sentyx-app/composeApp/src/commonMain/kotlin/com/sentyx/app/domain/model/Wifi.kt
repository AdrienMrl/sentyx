package com.sentyx.app.domain.model

/**
 * The Pi's Wi-Fi state as reported over BLE. [current] is null when the Pi is
 * offline; [saved] lists provisioned networks (one may be [SavedWifiNetwork.active]).
 * Scan results reuse [WifiNetwork].
 */
data class WifiStatus(
    val current: CurrentWifi?,
    val saved: List<SavedWifiNetwork>,
)

/** The network the Pi is associated with right now; [signal] is 0-100. */
data class CurrentWifi(
    val ssid: String,
    val signal: Int,
)

/** A network saved in the Pi's supplicant. [active] marks the one it's on now. */
data class SavedWifiNetwork(
    val ssid: String,
    val active: Boolean,
    val autoconnect: Boolean,
)

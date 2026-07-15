package com.sentyx.app.domain.model

/** A Sentyx Pi found during BLE scan. */
data class DiscoveredDevice(val id: String, val name: String, val subtitle: String)

sealed interface ScanState {
    data object Scanning : ScanState
    data class Found(val devices: List<DiscoveredDevice>) : ScanState
    data object NoneFound : ScanState
}

/** A Wi-Fi network visible to the Pi during setup. */
data class WifiNetwork(
    val ssid: String,
    val subtitle: String,
    val requiresPassword: Boolean,
)

/** App-level permission needed during onboarding (BLE, local network, notifications). */
data class OnboardingPermission(
    val key: String,
    val icon: String,
    val title: String,
    val subtitle: String,
    val granted: Boolean,
)

/** Hardware checklist item on the pairing intro screen. */
data class HardwareRequirement(val icon: String, val title: String, val subtitle: String)

/** Firmware update offer shown during onboarding or from device settings. */
data class FirmwareUpdate(
    val currentVersion: String,
    val newVersion: String,
    val summary: String,
    val detailLine: String,
)

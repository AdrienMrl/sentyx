package com.sentyx.app.domain.model

/**
 * A Sentyx Pi offered as a pairing target.
 *
 * [paired] marks a device that is already bonded to this phone. Those are
 * surfaced even when they no longer advertise, and are connected directly by
 * identifier rather than from a live advertisement.
 */
data class DiscoveredDevice(
    val id: String,
    val name: String,
    val subtitle: String,
    val paired: Boolean,
)

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

/**
 * How far the device has got with an update. Mirrors the states the unit's
 * updater reports to the backend, so the app never invents progress of its own.
 *
 * [WaitingSafe] is the one that needs explaining to users: the install is
 * downloaded and verified but deliberately held back until the car is not
 * recording, so it can sit there for hours without anything being wrong.
 */
enum class FirmwareStage(val label: String) {
    /** Offered to this device; nothing started yet. */
    Available("Update available"),

    /** Requested from the app; the device has not reported back yet. */
    Requested("Scheduled"),
    Downloading("Downloading"),

    /** Downloaded and verified, waiting for a moment when recording is idle. */
    WaitingSafe("Waiting for a safe moment"),
    Installing("Installing"),
    Rebooting("Restarting"),

    /** Installed, then reverted automatically after the new version failed its healthcheck. */
    RolledBack("Reverted after a failed check"),
    Failed("Update failed"),
}

/**
 * Firmware update offer shown during onboarding, from device settings, or on
 * the home screen. [progressPct] and [errorMessage] are only meaningful for the
 * stages that report them, and are left at their empty values otherwise.
 */
data class FirmwareUpdate(
    val currentVersion: String,
    val newVersion: String,
    val summary: String,
    val detailLine: String,
    val stage: FirmwareStage = FirmwareStage.Available,
    val progressPct: Int = 0,
    val errorMessage: String? = null,
) {
    /** The device is working on it; the app has nothing to offer but a status. */
    val inProgress: Boolean
        get() = stage == FirmwareStage.Requested ||
            stage == FirmwareStage.Downloading ||
            stage == FirmwareStage.WaitingSafe ||
            stage == FirmwareStage.Installing ||
            stage == FirmwareStage.Rebooting

    /** The user can start it — either for the first time, or retry after a failure. */
    val actionable: Boolean
        get() = stage == FirmwareStage.Available ||
            stage == FirmwareStage.Failed ||
            stage == FirmwareStage.RolledBack
}

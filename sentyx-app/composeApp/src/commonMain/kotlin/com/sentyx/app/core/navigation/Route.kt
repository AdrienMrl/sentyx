package com.sentyx.app.core.navigation

/**
 * Every screen in the app. Tab roots are marked so the shell knows when to
 * show the bottom bar.
 */
sealed interface Route {
    val isTabRoot: Boolean get() = false

    // Onboarding / auth
    data object Welcome : Route
    data object SignIn : Route
    data object CreateAccount : Route
    data object VerifyEmail : Route
    data object ResetPassword : Route

    // Pairing flow
    data object PairIntro : Route
    data object Permissions : Route
    data object Scan : Route
    data object NameDevice : Route
    data object WifiSetup : Route
    data object ConnectionTest : Route
    data object OnboardingFirmware : Route
    data object PairingDone : Route

    // Tab roots
    data object Events : Route { override val isTabRoot get() = true }
    data object Device : Route { override val isTabRoot get() = true }
    data object Transfers : Route { override val isTabRoot get() = true }
    data object Settings : Route { override val isTabRoot get() = true }

    // Events
    data class EventDetail(val eventId: String) : Route

    // Transfers
    data object DownloadLibrary : Route
    data object TransferSettings : Route

    // Device
    data object DeviceSettings : Route
    data object FirmwareFlow : Route
    data object Diagnostics : Route
    data object FactoryReset : Route
    data object AddDevice : Route

    // Notifications
    data object Alerts : Route
    data object QuietHours : Route
    data object NotificationHistory : Route

    // Settings
    data object Account : Route
    data object Security : Route
    data object Sessions : Route
    data object Privacy : Route
    data object AppSettings : Route
    data object PermissionStatus : Route
    data object Subscription : Route
    data object PrototypeStates : Route
}

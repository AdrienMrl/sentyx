package com.sentyx.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.Modifier
import androidx.compose.ui.backhandler.BackHandler
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.viewmodel.compose.viewModel
import com.sentyx.app.core.designsystem.SentyxTheme
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxToastBar
import com.sentyx.app.core.navigation.Navigator
import com.sentyx.app.core.navigation.Route
import com.sentyx.app.core.permissions.LocalPermissionsController
import com.sentyx.app.data.demo.DemoAppContainer
import com.sentyx.app.di.AppContainer
import com.sentyx.app.di.BuildFlavor
import com.sentyx.app.di.LocalAppContainer
import com.sentyx.app.di.RealAppContainer
import com.sentyx.app.domain.model.TransferStatus
import com.sentyx.app.feature.device.AddDeviceScreen
import com.sentyx.app.feature.device.DeviceScreen
import com.sentyx.app.feature.device.DeviceSettingsScreen
import com.sentyx.app.feature.device.DeviceViewModel
import com.sentyx.app.feature.device.DiagnosticsScreen
import com.sentyx.app.feature.device.FactoryResetScreen
import com.sentyx.app.feature.device.FirmwareFlowScreen
import com.sentyx.app.feature.events.EventDetailScreen
import com.sentyx.app.feature.events.EventDetailViewModel
import com.sentyx.app.feature.events.EventsFeedScreen
import com.sentyx.app.feature.events.EventsFeedViewModel
import com.sentyx.app.feature.notifications.AlertsScreen
import com.sentyx.app.feature.notifications.NotificationHistoryScreen
import com.sentyx.app.feature.notifications.NotificationsViewModel
import com.sentyx.app.feature.notifications.QuietHoursScreen
import com.sentyx.app.feature.onboarding.AuthViewModel
import com.sentyx.app.feature.onboarding.CreateAccountScreen
import com.sentyx.app.feature.onboarding.ResetPasswordScreen
import com.sentyx.app.feature.onboarding.SignInScreen
import com.sentyx.app.feature.onboarding.VerifyEmailScreen
import com.sentyx.app.feature.onboarding.WelcomeScreen
import com.sentyx.app.feature.pairing.ConnectionTestScreen
import com.sentyx.app.feature.pairing.NameDeviceScreen
import com.sentyx.app.feature.pairing.OnboardingFirmwareScreen
import com.sentyx.app.feature.pairing.PairIntroScreen
import com.sentyx.app.feature.pairing.PairPermissionsScreen
import com.sentyx.app.feature.pairing.PairingDoneScreen
import com.sentyx.app.feature.pairing.PairingViewModel
import com.sentyx.app.feature.pairing.ScanScreen
import com.sentyx.app.feature.pairing.WifiSetupScreen
import com.sentyx.app.feature.settings.AccountScreen
import com.sentyx.app.feature.settings.AccountViewModel
import com.sentyx.app.feature.settings.AppSettingsScreen
import com.sentyx.app.feature.settings.AppSettingsViewModel
import com.sentyx.app.feature.settings.PermissionStatusScreen
import com.sentyx.app.feature.settings.PermissionStatusViewModel
import com.sentyx.app.feature.settings.PrivacyScreen
import com.sentyx.app.feature.settings.PrivacyViewModel
import com.sentyx.app.feature.settings.PrototypeStatesScreen
import com.sentyx.app.feature.settings.PrototypeStatesViewModel
import com.sentyx.app.feature.settings.SavedWifiNetworksScreen
import com.sentyx.app.feature.settings.SavedWifiNetworksViewModel
import com.sentyx.app.feature.settings.SecurityScreen
import com.sentyx.app.feature.settings.SessionsScreen
import com.sentyx.app.feature.settings.SettingsHubScreen
import com.sentyx.app.feature.settings.SettingsHubViewModel
import com.sentyx.app.feature.settings.SubscriptionScreen
import com.sentyx.app.feature.settings.SubscriptionViewModel
import com.sentyx.app.feature.transfers.DownloadLibraryScreen
import com.sentyx.app.feature.transfers.TransferSettingsScreen
import com.sentyx.app.feature.transfers.TransfersScreen
import com.sentyx.app.feature.transfers.TransfersViewModel

/**
 * Root of the Sentyx app shell: creates the demo container, provides it via
 * [LocalAppContainer], hosts the [Navigator], renders the current [Route] to
 * its screen (+ ViewModels), and overlays the bottom tab bar and toast host.
 */
@OptIn(ExperimentalComposeUiApi::class)
@Composable
fun AppRoot() {
    val scope = rememberCoroutineScope()
    val container = remember {
        // Compile-time flavor switch (see di/BuildFlavor.kt). Default is demo so
        // the app works out of the box; the real flavor fails fast if AppConfig
        // was left unset.
        if (BuildFlavor.useRealPairing) {
            RealAppContainer(scope, BuildFlavor.requireRealConfig())
        } else {
            DemoAppContainer(scope)
        }
    }

    CompositionLocalProvider(LocalAppContainer provides container) {
        SentyxTheme {
            Surface(modifier = Modifier.fillMaxSize(), color = SxColors.Bg) {
                Box(Modifier.fillMaxSize()) {
                    val navigator = remember { Navigator(Route.Welcome) }

                    // System back pops the navigator stack; when the navigator has
                    // nothing to handle (Events root, Welcome), the system default
                    // applies and the app exits.
                    BackHandler(enabled = navigator.handlesBack) { navigator.back() }

                    Column(Modifier.fillMaxSize()) {
                        Box(Modifier.weight(1f).fillMaxWidth()) {
                            RouteContent(navigator, container)
                        }
                        if (navigator.showTabBar) {
                            TabBar(navigator, container)
                        }
                    }

                    ToastHost(container)
                }
            }
        }
    }
}

/** How the shared [Route.ConnectionTest] screen should behave on "continue". */
private enum class ConnTestMode { Pairing, Diagnostics }

@Composable
private fun RouteContent(navigator: Navigator, c: AppContainer) {
    // Route.ConnectionTest is reached from two places with different "continue"
    // semantics (pairing → OnboardingFirmware; diagnostics → back). Remember
    // which entry point navigated there so the branch can dispatch correctly.
    var connTestMode by remember { mutableStateOf(ConnTestMode.Pairing) }

    // Platform-provided Bluetooth permission bridge (Android: Activity Result;
    // iOS: CoreBluetooth). See LocalPermissionsController.
    val permissions = LocalPermissionsController.current

    when (val route = navigator.current) {
        // ---------- Onboarding / auth ----------
        Route.Welcome -> WelcomeScreen(
            onCreateAccount = { navigator.go(Route.CreateAccount) },
            onSignIn = { navigator.go(Route.SignIn) },
        )

        Route.SignIn -> SignInScreen(
            vm = viewModel { AuthViewModel(c.auth, c.toasts) },
            onBack = { navigator.back() },
            onForgotPassword = { navigator.go(Route.ResetPassword) },
            onSignedIn = { navigator.reset(Route.Events) },
        )

        Route.CreateAccount -> CreateAccountScreen(
            vm = viewModel { AuthViewModel(c.auth, c.toasts) },
            onBack = { navigator.back() },
            onContinueToVerify = { navigator.go(Route.VerifyEmail) },
        )

        Route.VerifyEmail -> VerifyEmailScreen(
            vm = viewModel { AuthViewModel(c.auth, c.toasts) },
            onBack = { navigator.back() },
            onVerified = { navigator.go(Route.PairIntro) },
        )

        Route.ResetPassword -> ResetPasswordScreen(
            vm = viewModel { AuthViewModel(c.auth, c.toasts) },
            onBack = { navigator.back() },
        )

        // ---------- Pairing flow ----------
        Route.PairIntro -> PairIntroScreen(
            vm = viewModel { PairingViewModel(c.pairing, c.toasts, permissions) },
            onBack = { navigator.back() },
            onContinue = { navigator.go(Route.Permissions) },
        )

        Route.Permissions -> PairPermissionsScreen(
            vm = viewModel { PairingViewModel(c.pairing, c.toasts, permissions) },
            onBack = { navigator.back() },
            onContinue = { navigator.go(Route.Scan) },
        )

        Route.Scan -> ScanScreen(
            vm = viewModel { PairingViewModel(c.pairing, c.toasts, permissions) },
            onBack = { navigator.back() },
            onDeviceSelected = { navigator.go(Route.NameDevice) },
        )

        Route.NameDevice -> NameDeviceScreen(
            vm = viewModel { PairingViewModel(c.pairing, c.toasts, permissions) },
            onBack = { navigator.back() },
            onContinue = { navigator.go(Route.WifiSetup) },
        )

        Route.WifiSetup -> WifiSetupScreen(
            vm = viewModel { PairingViewModel(c.pairing, c.toasts, permissions) },
            onBack = { navigator.back() },
            onContinue = {
                connTestMode = ConnTestMode.Pairing
                navigator.go(Route.ConnectionTest)
            },
        )

        Route.ConnectionTest -> ConnectionTestScreen(
            vm = viewModel { PairingViewModel(c.pairing, c.toasts, permissions) },
            onContinue = {
                when (connTestMode) {
                    ConnTestMode.Pairing -> navigator.go(Route.OnboardingFirmware)
                    ConnTestMode.Diagnostics -> navigator.back()
                }
            },
        )

        Route.OnboardingFirmware -> OnboardingFirmwareScreen(
            vm = viewModel { PairingViewModel(c.pairing, c.toasts, permissions) },
            onContinue = { navigator.go(Route.PairingDone) },
        )

        Route.PairingDone -> PairingDoneScreen(
            vm = viewModel { PairingViewModel(c.pairing, c.toasts, permissions) },
            onEnterApp = { navigator.reset(Route.Events) },
        )

        // ---------- Events (tab root) ----------
        Route.Events -> EventsFeedScreen(
            vm = viewModel { EventsFeedViewModel(c.events, c.device, c.toasts, c.thumbnails) },
            onOpenEvent = { id -> navigator.go(Route.EventDetail(id)) },
            onOpenDevice = { navigator.switchTab(Route.Device) },
            onGoToTransfers = { navigator.switchTab(Route.Transfers) },
        )

        is Route.EventDetail -> EventDetailScreen(
            vm = viewModel(key = "detail-" + route.eventId) {
                EventDetailViewModel(route.eventId, c.events, c.transfers, c.device, c.toasts)
            },
            onBack = { navigator.back() },
            onGoToTransfers = { navigator.switchTab(Route.Transfers) },
        )

        // ---------- Transfers (tab root) ----------
        Route.Transfers -> TransfersScreen(
            vm = viewModel { TransfersViewModel(c.transfers, c.toasts) },
            onOpenLibrary = { navigator.go(Route.DownloadLibrary) },
            onOpenSettings = { navigator.go(Route.TransferSettings) },
        )

        Route.DownloadLibrary -> DownloadLibraryScreen(
            vm = viewModel { TransfersViewModel(c.transfers, c.toasts) },
            onBack = { navigator.back() },
        )

        Route.TransferSettings -> TransferSettingsScreen(
            vm = viewModel { TransfersViewModel(c.transfers, c.toasts) },
            onBack = { navigator.back() },
        )

        // ---------- Device (tab root) ----------
        Route.Device -> DeviceScreen(
            vm = viewModel { DeviceViewModel(c.device, c.toasts) },
            onOpenSettings = { navigator.go(Route.DeviceSettings) },
            onOpenFirmware = { navigator.go(Route.FirmwareFlow) },
            onAddDevice = { navigator.go(Route.AddDevice) },
        )

        Route.DeviceSettings -> DeviceSettingsScreen(
            vm = viewModel { DeviceViewModel(c.device, c.toasts) },
            onBack = { navigator.back() },
            onOpenFirmware = { navigator.go(Route.FirmwareFlow) },
            onOpenDiagnostics = { navigator.go(Route.Diagnostics) },
            onOpenWifi = { navigator.go(Route.SavedWifiNetworks) },
            onFactoryReset = { navigator.go(Route.FactoryReset) },
        )

        Route.SavedWifiNetworks -> SavedWifiNetworksScreen(
            vm = viewModel { SavedWifiNetworksViewModel(c.deviceWifi, c.toasts) },
            onBack = { navigator.back() },
        )

        Route.FirmwareFlow -> FirmwareFlowScreen(
            vm = viewModel { DeviceViewModel(c.device, c.toasts) },
            onBack = { navigator.back() },
        )

        Route.Diagnostics -> DiagnosticsScreen(
            vm = viewModel { DeviceViewModel(c.device, c.toasts) },
            onBack = { navigator.back() },
            onRunTest = {
                connTestMode = ConnTestMode.Diagnostics
                navigator.go(Route.ConnectionTest)
            },
        )

        Route.FactoryReset -> FactoryResetScreen(
            vm = viewModel { DeviceViewModel(c.device, c.toasts) },
            onBack = { navigator.back() },
        )

        Route.AddDevice -> AddDeviceScreen(
            vm = viewModel { DeviceViewModel(c.device, c.toasts) },
            onBack = { navigator.back() },
            onPairNew = { navigator.go(Route.PairIntro) },
        )

        // ---------- Settings (tab root) ----------
        Route.Settings -> SettingsHubScreen(
            vm = viewModel { SettingsHubViewModel(c.auth, c.subscription, c.toasts) },
            onOpenDevice = { navigator.go(Route.Device) },
            onOpenDeviceSettings = { navigator.go(Route.DeviceSettings) },
            onOpenWifi = { navigator.go(Route.SavedWifiNetworks) },
            onOpenTransferSettings = { navigator.go(Route.TransferSettings) },
            onOpenAlerts = { navigator.go(Route.Alerts) },
            onOpenSubscription = { navigator.go(Route.Subscription) },
            onOpenAppSettings = { navigator.go(Route.AppSettings) },
            onOpenAccount = { navigator.go(Route.Account) },
            onOpenPrivacy = { navigator.go(Route.Privacy) },
            onOpenPrototypeStates = { navigator.go(Route.PrototypeStates) },
            onSignOut = { navigator.reset(Route.Welcome) },
        )

        Route.Account -> AccountScreen(
            vm = viewModel { AccountViewModel(c.auth, c.subscription, c.toasts) },
            onBack = { navigator.back() },
            onOpenSecurity = { navigator.go(Route.Security) },
            onOpenSessions = { navigator.go(Route.Sessions) },
            onOpenSubscription = { navigator.go(Route.Subscription) },
        )

        Route.Security -> SecurityScreen(
            vm = viewModel { AccountViewModel(c.auth, c.subscription, c.toasts) },
            onBack = { navigator.back() },
            onOpenSessions = { navigator.go(Route.Sessions) },
        )

        Route.Sessions -> SessionsScreen(
            vm = viewModel { AccountViewModel(c.auth, c.subscription, c.toasts) },
            onBack = { navigator.back() },
        )

        Route.Privacy -> PrivacyScreen(
            vm = viewModel { PrivacyViewModel(c.toasts) },
            onBack = { navigator.back() },
            onOpenPermissionStatus = { navigator.go(Route.PermissionStatus) },
        )

        Route.AppSettings -> AppSettingsScreen(
            vm = viewModel { AppSettingsViewModel(c.appSettings, c.toasts) },
            onBack = { navigator.back() },
        )

        Route.PermissionStatus -> PermissionStatusScreen(
            vm = viewModel { PermissionStatusViewModel(c.appSettings, c.toasts) },
            onBack = { navigator.back() },
        )

        Route.Subscription -> SubscriptionScreen(
            vm = viewModel { SubscriptionViewModel(c.subscription, c.toasts) },
            onBack = { navigator.back() },
        )

        // ---------- Notifications ----------
        Route.Alerts -> AlertsScreen(
            vm = viewModel { NotificationsViewModel(c.notifications, c.toasts) },
            onBack = { navigator.back() },
            onOpenQuietHours = { navigator.go(Route.QuietHours) },
            onOpenHistory = { navigator.go(Route.NotificationHistory) },
        )

        Route.QuietHours -> QuietHoursScreen(
            vm = viewModel { NotificationsViewModel(c.notifications, c.toasts) },
            onBack = { navigator.back() },
        )

        Route.NotificationHistory -> NotificationHistoryScreen(
            vm = viewModel { NotificationsViewModel(c.notifications, c.toasts) },
            onBack = { navigator.back() },
            onOpenEvent = { id -> navigator.go(Route.EventDetail(id)) },
        )

        // ---------- Prototype states (demo-only) ----------
        Route.PrototypeStates -> {
            val demoState = c.demoState
                ?: error("PrototypeStatesScreen requires AppContainer.demoState, but it is null. Use a demo container (DemoAppContainer).")
            PrototypeStatesScreen(
                vm = viewModel { PrototypeStatesViewModel(demoState, c.toasts) },
                onBack = { navigator.back() },
                onOpenEvent = { id -> navigator.go(Route.EventDetail(id)) },
            )
        }
    }
}

/**
 * Bottom tab bar (cream surface, 1px top border). Badges are observed directly
 * from the container repos: red dot on Device when the snapshot reports issues,
 * on Transfers when any transfer is actively transferring.
 */
@Composable
private fun TabBar(navigator: Navigator, container: AppContainer) {
    val snapshot by container.device.device.collectAsState()
    val transfers by container.transfers.transfers.collectAsState()

    val deviceBadge = snapshot?.issues?.isNotEmpty() == true
    val transfersBadge = transfers.any { it.status == TransferStatus.Transferring }
    val current = navigator.current

    Column(
        Modifier
            .fillMaxWidth()
            .background(Color(0xFFFBF8F2)),
    ) {
        Box(Modifier.fillMaxWidth().height(1.dp).background(Color(0xFFE7E0D2)))
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(start = 16.dp, end = 16.dp, top = 9.dp, bottom = 26.dp),
            horizontalArrangement = Arrangement.SpaceAround,
        ) {
            TabItem("◎", "Events", active = current == Route.Events, badge = false) {
                navigator.switchTab(Route.Events)
            }
            TabItem("⬡", "Device", active = current == Route.Device, badge = deviceBadge) {
                navigator.switchTab(Route.Device)
            }
            TabItem("↓", "Transfers", active = current == Route.Transfers, badge = transfersBadge) {
                navigator.switchTab(Route.Transfers)
            }
            TabItem("⚙", "Settings", active = current == Route.Settings, badge = false) {
                navigator.switchTab(Route.Settings)
            }
        }
    }
}

@Composable
private fun RowScope.TabItem(
    icon: String,
    label: String,
    active: Boolean,
    badge: Boolean,
    onClick: () -> Unit,
) {
    val color = if (active) Color(0xFF2B271F) else Color(0xFFB0A892)
    Box(
        modifier = Modifier
            .clickable(onClick = onClick)
            .padding(horizontal = 6.dp),
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(4.dp),
        ) {
            Text(icon, fontSize = 19.sp, color = color)
            Text(label, fontSize = 10.sp, fontWeight = FontWeight.Bold, color = color)
        }
        if (badge) {
            Box(
                Modifier
                    .align(Alignment.TopEnd)
                    .offset(x = 2.dp, y = (-3).dp)
                    .size(8.dp)
                    .clip(CircleShape)
                    .background(Color(0xFFB23A2E)),
            )
        }
    }
}

/** Top toast overlay: shows the container's current toast, dismissing on tap. */
@Composable
private fun BoxScope.ToastHost(container: AppContainer) {
    val toast by container.toasts.toast.collectAsState()
    val current = toast ?: return
    SxToastBar(
        toast = current.copy(
            onTap = {
                current.onTap?.invoke()
                container.toasts.dismiss()
            },
        ),
        modifier = Modifier
            .align(Alignment.TopCenter)
            .padding(top = 56.dp, start = 12.dp, end = 12.dp),
    )
}

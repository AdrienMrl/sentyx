package com.sentyx.app.data.demo

import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.di.AppContainer
import com.sentyx.app.domain.repository.AppSettingsRepository
import com.sentyx.app.domain.repository.AuthRepository
import com.sentyx.app.domain.repository.DeviceRepository
import com.sentyx.app.domain.repository.EventRepository
import com.sentyx.app.domain.repository.NotificationSettingsRepository
import com.sentyx.app.domain.repository.PairingService
import com.sentyx.app.domain.repository.SubscriptionRepository
import com.sentyx.app.domain.repository.TransferRepository
import kotlinx.coroutines.CoroutineScope

/**
 * Composition root for demo builds. Wires the demo repositories as lazy
 * singletons, all sharing a single [DemoStateController] (so the "Prototype
 * states" screen mutates every dependent screen at once) and one
 * [ToastController] bound to the app scope.
 */
class DemoAppContainer(private val scope: CoroutineScope) : AppContainer {

    private val demoStateController = DemoStateController()

    override val demoState: DemoStateController get() = demoStateController

    override val toasts: ToastController by lazy { ToastController(scope) }

    override val events: EventRepository by lazy {
        DemoEventRepository(scope, demoStateController)
    }

    override val device: DeviceRepository by lazy {
        DemoDeviceRepository(scope, demoStateController)
    }

    override val transfers: TransferRepository by lazy {
        DemoTransferRepository(scope, device)
    }

    override val auth: AuthRepository by lazy { DemoAuthRepository() }

    override val notifications: NotificationSettingsRepository by lazy {
        DemoNotificationSettingsRepository()
    }

    override val subscription: SubscriptionRepository by lazy {
        DemoSubscriptionRepository(scope, demoStateController)
    }

    override val appSettings: AppSettingsRepository by lazy { DemoAppSettingsRepository() }

    override val pairing: PairingService by lazy { DemoPairingService(demoStateController) }
}

package com.sentyx.app.di

import com.sentyx.app.core.push.PushTokenProvider
import com.sentyx.app.core.storage.KeyValueStore
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.data.auth.SupabaseAuthRepository
import com.sentyx.app.data.ble.BleDeviceWifiService
import com.sentyx.app.data.ble.BlePairingService
import com.sentyx.app.data.demo.DemoAppSettingsRepository
import com.sentyx.app.data.device.RealDeviceRepository
import com.sentyx.app.data.event.RealEventRepository
import com.sentyx.app.data.notifications.RealNotificationSettingsRepository
import com.sentyx.app.data.push.PushAwareAuthRepository
import com.sentyx.app.data.push.PushController
import com.sentyx.app.data.thumbnail.EventThumbnailLoader
import com.sentyx.app.data.thumbnail.SentyxThumbnailLoader
import com.sentyx.app.data.demo.DemoStateController
import com.sentyx.app.data.demo.DemoSubscriptionRepository
import com.sentyx.app.data.demo.DemoTransferRepository
import com.sentyx.app.domain.repository.AppSettingsRepository
import com.sentyx.app.domain.repository.AuthRepository
import com.sentyx.app.domain.repository.DeviceRepository
import com.sentyx.app.domain.repository.DeviceWifiService
import com.sentyx.app.domain.repository.EventRepository
import com.sentyx.app.domain.repository.NotificationSettingsRepository
import com.sentyx.app.domain.repository.PairingService
import com.sentyx.app.domain.repository.SubscriptionRepository
import com.sentyx.app.domain.repository.TransferRepository
import kotlinx.coroutines.CoroutineScope

/**
 * Real composition root: [pairing] is the live BLE + backend implementation,
 * [device] polls the backend for real Pi status (see [RealDeviceRepository]),
 * and [events] polls the backend event feed (see [RealEventRepository]); the
 * remaining repositories still reuse the demo implementation (real transfer
 * wiring lands in a later step). [demoState] is null per the
 * [AppContainer] contract, so demo-only UI (the "Prototype states" screen) is
 * unavailable in this flavor.
 *
 * The reused demo repositories still need a [DemoStateController], so one is
 * created privately here; it is intentionally NOT exposed via [demoState].
 */
class RealAppContainer(
    private val scope: CoroutineScope,
    private val config: AppConfig,
) : AppContainer {

    private val demoStateController = DemoStateController()

    // One key/value store shared by pairing (writes the paired device id) and
    // the device repository (reads it, polls status).
    private val keyValueStore = KeyValueStore()

    // Supabase auth + backend client: process-wide singletons ([RealServices]),
    // shared with the Android FCM service — refresh tokens are single-use, so
    // there must never be a second SupabaseSessionManager in the process.
    private val supabaseAuthClient = RealServices.authClient
    private val sessionManager = RealServices.sessionManager
    private val api = RealServices.api

    // Push-token lifecycle: registers on sign-in / app start (via the session
    // flow) and deletes on sign-out (via the auth decorator below). On iOS the
    // provider yields no token, so this is inert.
    private val pushController = PushController(
        scope = scope,
        api = api,
        session = sessionManager.session,
        tokenProvider = PushTokenProvider(),
    )

    init {
        pushController.start()
    }

    override val demoState: DemoStateController? = null

    override val toasts: ToastController by lazy { ToastController(scope) }

    override val thumbnails: EventThumbnailLoader by lazy { SentyxThumbnailLoader(api) }

    override val events: EventRepository by lazy {
        RealEventRepository(scope, api, keyValueStore)
    }

    override val device: DeviceRepository by lazy {
        RealDeviceRepository(scope, api, keyValueStore)
    }

    override val transfers: TransferRepository by lazy {
        DemoTransferRepository(scope, device)
    }

    override val auth: AuthRepository by lazy {
        PushAwareAuthRepository(
            delegate = SupabaseAuthRepository(scope, supabaseAuthClient, sessionManager),
            push = pushController,
        )
    }

    override val notifications: NotificationSettingsRepository by lazy {
        RealNotificationSettingsRepository(scope, api)
    }

    override val subscription: SubscriptionRepository by lazy {
        DemoSubscriptionRepository(scope, demoStateController)
    }

    override val appSettings: AppSettingsRepository by lazy { DemoAppSettingsRepository() }

    override val pairing: PairingService by lazy {
        BlePairingService(
            scope = scope,
            api = api,
            config = config,
            store = keyValueStore,
        )
    }

    override val deviceWifi: DeviceWifiService by lazy { BleDeviceWifiService(scope) }
}

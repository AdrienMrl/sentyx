package com.sentyx.app.di

import com.sentyx.app.core.storage.KeyValueStore
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.data.api.SentyxApi
import com.sentyx.app.data.ble.BleDeviceWifiService
import com.sentyx.app.data.ble.BlePairingService
import com.sentyx.app.data.demo.DemoAppSettingsRepository
import com.sentyx.app.data.demo.DemoAuthRepository
import com.sentyx.app.data.demo.DemoEventRepository
import com.sentyx.app.data.device.RealDeviceRepository
import com.sentyx.app.data.demo.DemoNotificationSettingsRepository
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
 * Real composition root: [pairing] is the live BLE + backend implementation and
 * [device] polls the backend for real Pi status (see [RealDeviceRepository]);
 * the remaining repositories still reuse the demo implementation (real
 * event/transfer wiring lands in a later step). [demoState] is null per the
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

    // One backend client and one key/value store shared by pairing (writes the
    // paired device id) and the device repository (reads it, polls status).
    private val api = SentyxApi(baseUrl = config.serverBaseUrl, operatorToken = config.operatorToken)
    private val keyValueStore = KeyValueStore()

    override val demoState: DemoStateController? = null

    override val toasts: ToastController by lazy { ToastController(scope) }

    override val events: EventRepository by lazy {
        DemoEventRepository(scope, demoStateController)
    }

    override val device: DeviceRepository by lazy {
        RealDeviceRepository(scope, api, keyValueStore)
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

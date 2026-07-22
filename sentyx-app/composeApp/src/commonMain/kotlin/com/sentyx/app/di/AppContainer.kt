package com.sentyx.app.di

import androidx.compose.runtime.staticCompositionLocalOf
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.data.clip.EventClipLoader
import com.sentyx.app.data.demo.DemoStateController
import com.sentyx.app.data.thumbnail.EventThumbnailLoader
import com.sentyx.app.domain.repository.AppSettingsRepository
import com.sentyx.app.domain.repository.AuthRepository
import com.sentyx.app.domain.repository.DeviceRepository
import com.sentyx.app.domain.repository.DeviceWifiService
import com.sentyx.app.domain.repository.EventRepository
import com.sentyx.app.domain.repository.NotificationSettingsRepository
import com.sentyx.app.domain.repository.PairingService
import com.sentyx.app.domain.repository.SubscriptionRepository
import com.sentyx.app.domain.repository.TransferRepository

/**
 * Composition root. Features depend on this interface (constructor-inject the
 * repositories they need); the app shell provides a concrete container.
 * Demo now (DemoAppContainer), real Pi/backend wiring later — no feature code
 * changes required.
 */
interface AppContainer {
    val events: EventRepository
    val device: DeviceRepository
    val transfers: TransferRepository
    val auth: AuthRepository
    val notifications: NotificationSettingsRepository
    val subscription: SubscriptionRepository
    val appSettings: AppSettingsRepository
    val pairing: PairingService
    val deviceWifi: DeviceWifiService
    val toasts: ToastController

    /** Decodes/caches event feed thumbnails; a no-op in demo builds. */
    val thumbnails: EventThumbnailLoader

    /** Builds authenticated clip sources for playback; a no-op in demo builds. */
    val clips: EventClipLoader

    /** Present only in demo builds; null once real data sources exist. */
    val demoState: DemoStateController?
}

val LocalAppContainer = staticCompositionLocalOf<AppContainer> {
    error("AppContainer not provided")
}

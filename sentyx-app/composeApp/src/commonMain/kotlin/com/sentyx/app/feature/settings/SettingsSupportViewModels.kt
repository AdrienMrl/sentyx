package com.sentyx.app.feature.settings

import androidx.lifecycle.ViewModel
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.core.ui.ToastData
import com.sentyx.app.core.ui.ToastTone
import com.sentyx.app.domain.model.AppPrefs
import com.sentyx.app.domain.model.PermissionStatus
import com.sentyx.app.domain.repository.AppSettingsRepository
import kotlinx.coroutines.flow.StateFlow

/** Emits the shared "not wired up" mock toast used by stub settings rows. */
private fun ToastController.mockAction() = show(
    ToastData(
        title = "Mock action",
        subtitle = "Not wired up in this prototype",
        tone = ToastTone.Info,
    ),
)

/**
 * Backs [AppSettingsScreen]. Exposes [AppSettingsRepository.prefs] (units, time
 * display, language); every row is a mock action in this prototype.
 */
class AppSettingsViewModel(
    appSettings: AppSettingsRepository,
    private val toasts: ToastController,
) : ViewModel() {
    val prefs: StateFlow<AppPrefs> = appSettings.prefs

    /** Any App-settings row tap (Units / Time / Language / Help / Support / Feedback). */
    fun stub() = toasts.mockAction()
}

/**
 * Backs [PermissionStatusScreen]. Exposes [AppSettingsRepository.permissions];
 * a denied row's "enable" affordance surfaces a mock toast.
 */
class PermissionStatusViewModel(
    appSettings: AppSettingsRepository,
    private val toasts: ToastController,
) : ViewModel() {
    val permissions: StateFlow<List<PermissionStatus>> = appSettings.permissions

    /** "Denied · enable ›" tap. */
    fun enable() = toasts.mockAction()
}

/**
 * Backs [PrivacyScreen]. A toast-only VM: the privacy rows (data retention,
 * export, delete cloud data, delete account) are all mock actions.
 */
class PrivacyViewModel(
    private val toasts: ToastController,
) : ViewModel() {
    fun stub() = toasts.mockAction()
}

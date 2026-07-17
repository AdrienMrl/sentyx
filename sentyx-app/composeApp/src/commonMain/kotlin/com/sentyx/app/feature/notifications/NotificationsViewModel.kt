package com.sentyx.app.feature.notifications

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.core.ui.ToastData
import com.sentyx.app.core.ui.ToastTone
import com.sentyx.app.domain.model.MinThreatLevel
import com.sentyx.app.domain.model.NotificationEntry
import com.sentyx.app.domain.model.NotificationRule
import com.sentyx.app.domain.model.QuietHours
import com.sentyx.app.domain.repository.NotificationSettingsRepository
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch

/** Immutable UI state for the notifications screens. */
data class NotificationsUiState(
    val minThreatLevel: MinThreatLevel,
    val rules: List<NotificationRule>,
    val quietHours: QuietHours,
    val hidePreviewContent: Boolean,
    val history: List<NotificationEntry>,
)

/**
 * Drives the alerts, quiet-hours, and notification-history screens. Reads the
 * repository's [NotificationSettingsRepository.minThreatLevel], [prefs], and
 * [history] flows and writes user changes back through the repository (single
 * source of truth). The threshold write is optimistic in the repository; a
 * failure rolls the flow back and this ViewModel surfaces a toast.
 */
class NotificationsViewModel(
    private val settings: NotificationSettingsRepository,
    private val toasts: ToastController,
) : ViewModel() {

    val state: StateFlow<NotificationsUiState> =
        combine(
            settings.minThreatLevel,
            settings.prefs,
            settings.history,
        ) { minThreatLevel, prefs, history ->
            NotificationsUiState(
                minThreatLevel = minThreatLevel,
                rules = prefs.rules,
                quietHours = prefs.quietHours,
                hidePreviewContent = prefs.hidePreviewContent,
                history = history,
            )
        }.stateIn(
            scope = viewModelScope,
            started = SharingStarted.WhileSubscribed(5_000),
            initialValue = NotificationsUiState(
                minThreatLevel = settings.minThreatLevel.value,
                rules = settings.prefs.value.rules,
                quietHours = settings.prefs.value.quietHours,
                hidePreviewContent = settings.prefs.value.hidePreviewContent,
                history = settings.history.value,
            ),
        )

    fun setMinThreatLevel(level: MinThreatLevel) {
        viewModelScope.launch {
            try {
                settings.setMinThreatLevel(level)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                // The repository has already rolled the flow back to its prior value.
                toasts.show(
                    ToastData(
                        title = "Couldn't update alerts",
                        subtitle = e.message ?: "Please try again",
                        tone = ToastTone.Urgent,
                    ),
                )
            }
        }
    }

    fun setRuleEnabled(key: String, enabled: Boolean) {
        viewModelScope.launch { settings.setRuleEnabled(key, enabled) }
    }

    fun setQuietHoursEnabled(enabled: Boolean) {
        val current = settings.prefs.value.quietHours
        viewModelScope.launch { settings.setQuietHours(current.copy(enabled = enabled)) }
    }

    fun setAllowUrgentDuringQuietHours(allow: Boolean) {
        val current = settings.prefs.value.quietHours
        viewModelScope.launch { settings.setQuietHours(current.copy(allowUrgent = allow)) }
    }

    fun setHidePreviewContent(hide: Boolean) {
        viewModelScope.launch { settings.setHidePreviewContent(hide) }
    }
}

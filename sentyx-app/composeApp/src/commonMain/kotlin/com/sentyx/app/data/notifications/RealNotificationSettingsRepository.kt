package com.sentyx.app.data.notifications

import com.sentyx.app.data.api.SentyxApi
import com.sentyx.app.domain.model.MinThreatLevel
import com.sentyx.app.domain.model.NotificationEntry
import com.sentyx.app.domain.model.NotificationPrefs
import com.sentyx.app.domain.model.QuietHours
import com.sentyx.app.domain.repository.NotificationSettingsRepository
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

/**
 * Real [NotificationSettingsRepository]. The push threshold is server-backed via
 * `/v1/me/notification-settings`: it is loaded once on construction and each
 * change is applied optimistically then persisted with `PUT`, rolling back and
 * rethrowing on failure so the ViewModel can surface a toast.
 *
 * The per-type rules, quiet hours, and history have no backend endpoints yet, so
 * they reuse the shared [NotificationSettingsDefaults] as in-memory state
 * (consistent with how other real repositories keep prototype surface local).
 */
class RealNotificationSettingsRepository(
    scope: CoroutineScope,
    private val api: SentyxApi,
) : NotificationSettingsRepository {

    // Seeded with a placeholder so the selector renders immediately; overwritten
    // by the first successful GET below. This is transient display state, not
    // configuration, so a pre-load placeholder is acceptable.
    private val _minThreatLevel = MutableStateFlow(NotificationSettingsDefaults.MIN_THREAT_LEVEL)
    override val minThreatLevel: StateFlow<MinThreatLevel> = _minThreatLevel

    private val _prefs = MutableStateFlow(
        NotificationPrefs(
            rules = NotificationSettingsDefaults.RULES,
            quietHours = NotificationSettingsDefaults.QUIET_HOURS,
            hidePreviewContent = false,
        ),
    )
    override val prefs: StateFlow<NotificationPrefs> = _prefs

    private val _history = MutableStateFlow<List<NotificationEntry>>(NotificationSettingsDefaults.HISTORY)
    override val history: StateFlow<List<NotificationEntry>> = _history

    init {
        scope.launch {
            try {
                _minThreatLevel.value = MinThreatLevel.fromWire(api.getNotificationSettings().minThreatLevel)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                // Keep the placeholder; the user can still change it (which re-syncs).
            }
        }
    }

    override suspend fun setMinThreatLevel(level: MinThreatLevel) {
        val previous = _minThreatLevel.value
        if (previous == level) return
        _minThreatLevel.value = level // optimistic
        try {
            api.putNotificationSettings(level.wire)
        } catch (e: CancellationException) {
            throw e
        } catch (e: Throwable) {
            _minThreatLevel.value = previous // rollback
            throw e
        }
    }

    override suspend fun setRuleEnabled(key: String, enabled: Boolean) {
        _prefs.value = _prefs.value.copy(
            rules = _prefs.value.rules.map {
                if (it.key == key) it.copy(enabled = enabled) else it
            },
        )
    }

    override suspend fun setQuietHours(quietHours: QuietHours) {
        _prefs.value = _prefs.value.copy(quietHours = quietHours)
    }

    override suspend fun setHidePreviewContent(hide: Boolean) {
        _prefs.value = _prefs.value.copy(hidePreviewContent = hide)
    }
}

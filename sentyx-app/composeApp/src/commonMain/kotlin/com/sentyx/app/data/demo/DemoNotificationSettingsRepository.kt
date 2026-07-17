package com.sentyx.app.data.demo

import com.sentyx.app.data.notifications.NotificationSettingsDefaults
import com.sentyx.app.domain.model.MinThreatLevel
import com.sentyx.app.domain.model.NotificationEntry
import com.sentyx.app.domain.model.NotificationPrefs
import com.sentyx.app.domain.model.QuietHours
import com.sentyx.app.domain.repository.NotificationSettingsRepository
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/**
 * Demo [NotificationSettingsRepository]. Keeps the push threshold in memory and
 * ports the prototype's notification rules, quiet-hours defaults, and history
 * from [NotificationSettingsDefaults]. Setters never throw.
 */
class DemoNotificationSettingsRepository : NotificationSettingsRepository {

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

    override suspend fun setMinThreatLevel(level: MinThreatLevel) {
        _minThreatLevel.value = level
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

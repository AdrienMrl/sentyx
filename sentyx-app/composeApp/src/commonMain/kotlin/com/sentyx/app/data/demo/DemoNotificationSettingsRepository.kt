package com.sentyx.app.data.demo

import com.sentyx.app.domain.model.NotificationEntry
import com.sentyx.app.domain.model.NotificationPrefs
import com.sentyx.app.domain.model.NotificationRule
import com.sentyx.app.domain.model.QuietHours
import com.sentyx.app.domain.model.Severity
import com.sentyx.app.domain.repository.NotificationSettingsRepository
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/**
 * Demo [NotificationSettingsRepository]. Ports the prototype's 17 notification
 * rules, quiet-hours defaults, and 4-entry history.
 */
class DemoNotificationSettingsRepository : NotificationSettingsRepository {

    private val _prefs = MutableStateFlow(
        NotificationPrefs(
            minSeverity = Severity.Attention,
            rules = DEFAULT_RULES,
            quietHours = QuietHours(
                enabled = true,
                start = "10:00 PM",
                end = "7:00 AM",
                days = "Every day",
                allowUrgent = true,
            ),
            hidePreviewContent = false,
        ),
    )
    override val prefs: StateFlow<NotificationPrefs> = _prefs

    private val _history = MutableStateFlow(
        listOf(
            NotificationEntry(
                title = "Urgent · Door contact",
                subtitle = "Yesterday 10:42 PM · tap to view",
                severity = Severity.Urgent,
                eventId = "e4",
            ),
            NotificationEntry(
                title = "Attention · Person at driver door",
                subtitle = "Today 2:14 PM",
                severity = Severity.Attention,
                eventId = "e1",
            ),
            NotificationEntry(
                title = "Device · Low storage warning",
                subtitle = "Today 1:02 PM",
                severity = Severity.Attention,
                eventId = null,
            ),
            NotificationEntry(
                title = "Routine · Pedestrian passed",
                subtitle = "Today 8:05 AM",
                severity = Severity.Routine,
                eventId = "e3",
            ),
        ),
    )
    override val history: StateFlow<List<NotificationEntry>> = _history

    override suspend fun setRuleEnabled(key: String, enabled: Boolean) {
        _prefs.value = _prefs.value.copy(
            rules = _prefs.value.rules.map {
                if (it.key == key) it.copy(enabled = enabled) else it
            },
        )
    }

    override suspend fun setMinSeverity(severity: Severity) {
        _prefs.value = _prefs.value.copy(minSeverity = severity)
    }

    override suspend fun setQuietHours(quietHours: QuietHours) {
        _prefs.value = _prefs.value.copy(quietHours = quietHours)
    }

    override suspend fun setHidePreviewContent(hide: Boolean) {
        _prefs.value = _prefs.value.copy(hidePreviewContent = hide)
    }

    private companion object {
        val DEFAULT_RULES = listOf(
            NotificationRule("urgent", "Urgent events", "Contact, break-in attempts", "Events", true),
            NotificationRule("attention", "Attention events", "Lingering, close approaches", "Events", true),
            NotificationRule("routine", "Routine events", "Passers-by, carts", "Events", false),
            NotificationRule("people", "People", "", "Types", true),
            NotificationRule("vehicles", "Vehicles", "", "Types", true),
            NotificationRule("animals", "Animals", "", "Types", false),
            NotificationRule("motion", "Motion", "", "Types", false),
            NotificationRule("contact", "Detected contact", "", "Types", true),
            NotificationRule("anDone", "Analysis complete", "", "System", true),
            NotificationRule("anFail", "Analysis failed", "", "System", false),
            NotificationRule("offline", "Device offline", "", "System", true),
            NotificationRule("reconnect", "Device reconnected", "", "System", false),
            NotificationRule("storage", "Low storage", "", "System", true),
            NotificationRule("power", "Weak power", "", "System", true),
            NotificationRule("heat", "Overheating", "", "System", true),
            NotificationRule("fw", "Firmware updates", "", "System", true),
            NotificationRule("transfer", "Transfer complete", "", "System", false),
        )
    }
}

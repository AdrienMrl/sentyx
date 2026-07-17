package com.sentyx.app.data.notifications

import com.sentyx.app.domain.model.MinThreatLevel
import com.sentyx.app.domain.model.NotificationEntry
import com.sentyx.app.domain.model.NotificationRule
import com.sentyx.app.domain.model.QuietHours
import com.sentyx.app.domain.model.Severity

/**
 * Shared seed data for the notification-settings screens, used by both the demo
 * repository and the real one. The per-type rules, quiet hours, and history are
 * still prototype surface — the backend exposes no endpoints for them — so the
 * real repository serves the same in-memory data as the demo (mirroring how
 * `SupabaseAuthRepository` still serves demo session rows). Only the push
 * threshold ([MinThreatLevel]) is server-backed.
 */
internal object NotificationSettingsDefaults {

    /** Placeholder threshold shown before the first server fetch resolves. */
    val MIN_THREAT_LEVEL: MinThreatLevel = MinThreatLevel.MediumAndUp

    val QUIET_HOURS = QuietHours(
        enabled = true,
        start = "10:00 PM",
        end = "7:00 AM",
        days = "Every day",
        allowUrgent = true,
    )

    val RULES = listOf(
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

    val HISTORY = listOf(
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
    )
}

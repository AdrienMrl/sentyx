package com.sentyx.app.domain.model

/** One toggleable notification kind, grouped by section on the alerts screen. */
data class NotificationRule(
    val key: String,
    val title: String,
    val subtitle: String,
    /** Section header: "Events", "Types", "System". */
    val section: String,
    val enabled: Boolean,
)

data class QuietHours(
    val enabled: Boolean,
    val start: String,
    val end: String,
    val days: String,
    /** Urgent events alert even during quiet hours. */
    val allowUrgent: Boolean,
)

data class NotificationPrefs(
    val minSeverity: Severity,
    val rules: List<NotificationRule>,
    val quietHours: QuietHours,
    val hidePreviewContent: Boolean,
)

/** A past notification, tappable when it references an event. */
data class NotificationEntry(
    val title: String,
    val subtitle: String,
    val severity: Severity,
    val eventId: String?,
)

/** App-level (non-device) preferences. */
data class AppPrefs(
    val units: String,
    val timeDisplay: String,
    val language: String,
)

/** Phone permission status for the privacy screen. */
data class PermissionStatus(val name: String, val state: PermissionState)

enum class PermissionState { Allowed, Denied, Ask }

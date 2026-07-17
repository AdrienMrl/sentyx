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
    val rules: List<NotificationRule>,
    val quietHours: QuietHours,
    val hidePreviewContent: Boolean,
)

/**
 * Push-notification threshold: the server pushes a verdict only when its threat
 * level is at least this high. Mirrors the server's `min_threat_level` contract
 * where the ordering is none < low < medium < high, and "off" means never.
 *
 * [wire] is the exact string the `/v1/me/notification-settings` endpoint accepts;
 * [label]/[detail] are the user-facing copy for the single-choice selector.
 */
enum class MinThreatLevel(val wire: String, val label: String, val detail: String) {
    /** Never notify. */
    Off("off", "Off", "Never notify me"),

    /** Only high-threat verdicts (threshold "high"). */
    HighOnly("high", "High only", "Only the most serious events"),

    /** Medium and high verdicts (threshold "medium"). */
    MediumAndUp("medium", "Medium and up", "Medium and high threats"),

    /** Low, medium, and high verdicts (threshold "low"). */
    LowAndUp("low", "Low and up", "Low, medium, and high threats"),

    /** Every analyzed verdict (threshold "none"). */
    Everything("none", "Everything", "Every analyzed event");

    companion object {
        /** Parse a server `min_threat_level` string; throws on an unknown value (no silent default). */
        fun fromWire(wire: String): MinThreatLevel = entries.firstOrNull { it.wire == wire }
            ?: throw IllegalArgumentException("Unknown min_threat_level: \"$wire\"")
    }
}

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

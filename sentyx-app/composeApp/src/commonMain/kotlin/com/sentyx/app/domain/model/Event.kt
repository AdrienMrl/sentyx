package com.sentyx.app.domain.model

/** Severity assigned to a Sentry event by analysis (or overridden by the user). */
enum class Severity { Urgent, Attention, Routine }

/** Lifecycle of an event's clip + analysis pipeline (car → Pi → backend → Gemini). */
enum class AnalysisState {
    /** Analysis finished; clip + verdict available. */
    Complete,
    /** The car is still writing the clip. */
    Writing,
    /** Clip is uploading from the Pi to the backend. */
    Uploading,
    /** Gemini is processing the clip. */
    Analyzing,
    /** Pi offline; clip will upload when it reconnects. */
    Waiting,
    /** Analysis failed; retryable. */
    Failed,
    /** Clip is encrypted by the car firmware and can't be analyzed. */
    Encrypted,
    /** Clip format not supported. */
    Unsupported,
}

/** A notable point on the clip timeline, e.g. "Door opens" at 0:33. */
data class Moment(
    val timestamp: String,
    val label: String,
    /** Position within the clip, 0..100, used for seek. */
    val positionPct: Int,
)

/** A Sentry Mode event as surfaced in the app. Immutable analysis output. */
data class SentryEvent(
    val id: String,
    /** Feed group label, e.g. "Today", "Yesterday", "Jul 12". */
    val dayLabel: String,
    val time: String,
    val location: String,
    /** Null when unknown (e.g. encrypted clips). */
    val severity: Severity?,
    val title: String,
    /** All camera angles captured for this event; first is the trigger camera. */
    val cameras: List<String>,
    val durationLabel: String,
    val state: AnalysisState,
    /** Analyzer confidence 0..100; null until analysis completes. */
    val confidencePct: Int? = null,
    /** One-line AI summary for the feed card. */
    val aiSummaryShort: String? = null,
    /** Full AI description for the detail screen. */
    val description: String? = null,
    /** Objective observations ("1 person detected", "No contact"). */
    val facts: List<String> = emptyList(),
    /** The AI's interpretation of the facts. */
    val interpretation: String? = null,
    /** Detected subjects, e.g. "Person", "Vehicle". */
    val subjects: List<String> = emptyList(),
    val contactDetected: Boolean = false,
    val moments: List<Moment> = emptyList(),
    /** Original clip size, e.g. "248 MB". */
    val originalSizeLabel: String = "248 MB",
    /** Remaining retention, null when not applicable. */
    val retentionOnCar: String? = null,
    val retentionCloud: String? = null,
)

/** User feedback on an analysis verdict. */
enum class AnalysisFeedback { Accurate, Inaccurate, FalseAlarm }

/** Mutable per-event user state, kept separate from the immutable analysis. */
data class EventUserMeta(
    val favorite: Boolean = false,
    val reviewed: Boolean = false,
    val severityOverride: Severity? = null,
    val feedback: AnalysisFeedback? = null,
)

/** An event joined with its user meta; what screens render. */
data class EventWithMeta(
    val event: SentryEvent,
    val meta: EventUserMeta,
) {
    /** Effective severity after any user override. */
    val severity: Severity? get() = meta.severityOverride ?: event.severity
}

/** Feed content states, mirroring backend/cache conditions. */
sealed interface FeedState {
    data object Loading : FeedState
    /** Groups ordered newest first. */
    data class Loaded(val groups: List<FeedGroup>) : FeedState
    data object Empty : FeedState
    /** Backend unreachable but cached events may still exist. */
    data object Offline : FeedState
    data object Error : FeedState
}

data class FeedGroup(val label: String, val events: List<EventWithMeta>)

/** Feed severity filter. */
enum class FeedFilter { All, Urgent, Attention, Routine, Favorites }

package com.sentyx.app.data.event

import com.sentyx.app.data.api.EventSummaryDto
import com.sentyx.app.domain.model.AnalysisState
import com.sentyx.app.domain.model.EventUserMeta
import com.sentyx.app.domain.model.EventWithMeta
import com.sentyx.app.domain.model.FeedGroup
import com.sentyx.app.domain.model.SentryEvent
import com.sentyx.app.domain.model.Severity
import kotlinx.datetime.LocalDate
import kotlinx.datetime.LocalDateTime
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/**
 * Pure mapping from the server's [EventSummaryDto] onto the app's [SentryEvent],
 * plus feed grouping. No IO, no clock access beyond the `today` passed in, so it
 * is fully unit-testable. Missing/blank server fields map to honest fallbacks
 * (null severity, "Unknown location") rather than fabricated values.
 */

/** The Gemini verdict document stored in `analysis_json`; every field optional. */
@Serializable
internal data class VerdictDto(
    @SerialName("concern_detected") val concernDetected: Boolean? = null,
    @SerialName("threat_level") val threatLevel: String? = null,
    @SerialName("what_happened") val whatHappened: String? = null,
    val evidence: String? = null,
    @SerialName("recommended_action") val recommendedAction: String? = null,
    @SerialName("event_timestamp_seconds") val eventTimestampSec: Int? = null,
)

private val verdictCodec = Json { ignoreUnknownKeys = true; isLenient = true }

/** Parse the raw verdict JSON; null/blank or malformed yields null (not-yet-analyzed). */
internal fun parseVerdict(analysisJson: String): VerdictDto? {
    if (analysisJson.isBlank()) return null
    return try {
        verdictCodec.decodeFromString(VerdictDto.serializer(), analysisJson)
    } catch (e: Exception) {
        null
    }
}

/**
 * Map the server's `threat_level` enum onto the app [Severity]. `high` is
 * Urgent, `medium` Attention, `low`/`none` Routine; blank/unknown yields null
 * (severity not yet known, e.g. pending analysis).
 */
internal fun severityOf(threatLevel: String): Severity? = when (threatLevel.lowercase()) {
    "high" -> Severity.Urgent
    "medium" -> Severity.Attention
    "low", "none" -> Severity.Routine
    else -> null
}

/**
 * Map `analysis_state` (+ finalize state / error) onto the app [AnalysisState].
 * An event that hasn't finalized yet is still Uploading even though its
 * analysis row reads `pending`; a `skipped` clip whose error mentions
 * encryption surfaces as Encrypted.
 */
internal fun analysisStateOf(dto: EventSummaryDto): AnalysisState {
    return when (dto.analysisState.lowercase()) {
        "done" -> AnalysisState.Complete
        "running" -> AnalysisState.Analyzing
        "failed" -> AnalysisState.Failed
        "skipped" ->
            if (dto.analysisError.contains("encrypt", ignoreCase = true)) AnalysisState.Encrypted
            else AnalysisState.Unsupported
        "pending" ->
            // Not finalized yet (no completed_at) → still arriving; else queued for Gemini.
            if (dto.completedAt.isNullOrBlank()) AnalysisState.Uploading else AnalysisState.Analyzing
        else -> AnalysisState.Analyzing
    }
}

/** Tesla event.json camera code → human label, mirroring the server's `cameraName`. */
internal fun cameraLabel(code: String): String? = when (code) {
    "" -> null
    "3", "5" -> "Left repeater"
    "4", "6" -> "Right repeater"
    "7" -> "Back"
    else -> "Front" // "0" front, "1" fisheye, "2" narrow, unknown
}

/** A short human title from the trigger reason, e.g. `sentry_aware_object_detection`. */
internal fun reasonTitle(reason: String): String? = when (reason) {
    "" -> null
    "sentry_aware_object_detection" -> "Object detected nearby"
    "sentry_aware_accel_detection" -> "Motion detected"
    "user_interaction_honk" -> "Horn triggered"
    else -> reason.replace('_', ' ').replaceFirstChar { it.uppercase() }
}

/** Zero-padded "H:MM AM/PM" from a parsed local time. */
internal fun timeLabel(dt: LocalDateTime): String {
    val h24 = dt.hour
    val meridiem = if (h24 < 12) "AM" else "PM"
    val h12 = when {
        h24 == 0 -> 12
        h24 > 12 -> h24 - 12
        else -> h24
    }
    val mm = dt.minute.toString().padStart(2, '0')
    return "$h12:$mm $meridiem"
}

private val MONTHS = listOf(
    "Jan", "Feb", "Mar", "Apr", "May", "Jun",
    "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
)

/** Feed group label for [date] relative to [today]: "Today"/"Yesterday"/"Jul 4". */
internal fun dayLabel(date: LocalDate, today: LocalDate): String {
    val delta = today.toEpochDays() - date.toEpochDays()
    return when (delta) {
        0L -> "Today"
        1L -> "Yesterday"
        else -> "${MONTHS[date.monthNumber - 1]} ${date.dayOfMonth}"
    }
}

/**
 * Parse the event's display timestamp. Prefer the car's local `event_ts`; fall
 * back to `first_seen` (an RFC3339 instant, whose "T…Z"/offset suffix we trim to
 * the local-ish wall-clock for labeling). Null if neither parses.
 */
internal fun eventLocalDateTime(dto: EventSummaryDto): LocalDateTime? {
    parseLocalDateTime(dto.eventTs)?.let { return it }
    val fs = dto.firstSeen ?: return null
    // Trim any zone designator so LocalDateTime.parse accepts it; this is only
    // for day/time labels, not ordering (ordering uses the raw string sort).
    val trimmed = fs.substringBefore('Z').substringBefore('+').take(19)
    return parseLocalDateTime(trimmed)
}

private fun parseLocalDateTime(s: String): LocalDateTime? {
    if (s.isBlank()) return null
    return try {
        LocalDateTime.parse(s)
    } catch (e: Exception) {
        null
    }
}

/**
 * Map one server row onto a [SentryEvent]. [today] fixes the "Today/Yesterday"
 * boundary (caller supplies it from the device's current local date).
 */
internal fun mapEvent(dto: EventSummaryDto, today: LocalDate): SentryEvent {
    val verdict = parseVerdict(dto.analysisJson)
    val ldt = eventLocalDateTime(dto)

    // Severity from the verdict when present, else the row's threat_level column.
    val severity = severityOf(verdict?.threatLevel ?: dto.threatLevel)

    val title = verdict?.whatHappened?.takeIf { it.isNotBlank() }
        ?: reasonTitle(dto.reason)
        ?: "Sentry event"

    val cameras = listOfNotNull(cameraLabel(dto.camera))

    return SentryEvent(
        id = dto.id,
        dayLabel = ldt?.let { dayLabel(it.date, today) } ?: "Earlier",
        time = ldt?.let { timeLabel(it) } ?: "",
        location = dto.city.ifBlank { "Unknown location" },
        severity = severity,
        title = title,
        cameras = cameras,
        durationLabel = "",
        state = analysisStateOf(dto),
        confidencePct = null,
        aiSummaryShort = verdict?.whatHappened?.takeIf { it.isNotBlank() },
        description = verdict?.whatHappened?.takeIf { it.isNotBlank() },
        facts = listOfNotNull(verdict?.evidence?.takeIf { it.isNotBlank() }),
        interpretation = verdict?.recommendedAction?.takeIf { it.isNotBlank() },
        subjects = emptyList(),
        contactDetected = false,
        moments = emptyList(),
        originalSizeLabel = "",
        retentionOnCar = null,
        retentionCloud = null,
    )
}

/**
 * Build the day-grouped feed from raw server rows. Rows are ordered newest-first
 * by `first_seen` (falling back to `event_ts`, both lexically sortable), then
 * grouped by day label preserving that order. [meta] supplies per-event user
 * annotations; missing entries default to an empty [EventUserMeta].
 */
internal fun buildFeedGroups(
    rows: List<EventSummaryDto>,
    meta: Map<String, EventUserMeta>,
    today: LocalDate,
): List<FeedGroup> {
    val sorted = rows.sortedByDescending { it.firstSeen ?: it.eventTs }
    val order = LinkedHashMap<String, MutableList<EventWithMeta>>()
    for (row in sorted) {
        val event = mapEvent(row, today)
        order.getOrPut(event.dayLabel) { mutableListOf() }
            .add(EventWithMeta(event, meta[event.id] ?: EventUserMeta()))
    }
    return order.map { (label, items) -> FeedGroup(label, items) }
}

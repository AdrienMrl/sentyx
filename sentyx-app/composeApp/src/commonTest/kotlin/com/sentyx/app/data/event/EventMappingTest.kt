package com.sentyx.app.data.event

import com.sentyx.app.data.api.EventSummaryDto
import com.sentyx.app.domain.model.AnalysisState
import com.sentyx.app.domain.model.EventUserMeta
import com.sentyx.app.domain.model.Severity
import kotlinx.datetime.LocalDate
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

class EventMappingTest {

    private val today = LocalDate(2026, 7, 15)

    private fun row(
        id: String = "evt1",
        firstSeen: String? = "2026-07-15T09:00:00Z",
        completedAt: String? = "2026-07-15T09:00:30Z",
        eventTs: String = "2026-07-15T09:00:00",
        city: String = "North Las Vegas",
        reason: String = "sentry_aware_object_detection",
        camera: String = "5",
        analysisState: String = "done",
        threatLevel: String = "",
        analysisJson: String = "",
        analysisError: String = "",
    ) = EventSummaryDto(
        id = id,
        firstSeen = firstSeen,
        completedAt = completedAt,
        eventTs = eventTs,
        city = city,
        reason = reason,
        camera = camera,
        analysisState = analysisState,
        threatLevel = threatLevel,
        analysisJson = analysisJson,
        analysisError = analysisError,
    )

    private val verdictHigh = """
        {"concern_detected":true,"threat_level":"high","what_happened":"A person struck the car.",
        "evidence":"Contact visible on the door.","recommended_action":"Review and report.",
        "event_timestamp_seconds":12}
    """.trimIndent()

    @Test
    fun mapsVerdictOntoEvent() {
        val e = mapEvent(row(analysisJson = verdictHigh), today)
        assertEquals(Severity.Urgent, e.severity)
        assertEquals("A person struck the car.", e.title)
        assertEquals("A person struck the car.", e.aiSummaryShort)
        assertEquals(listOf("Contact visible on the door."), e.facts)
        assertEquals("Review and report.", e.interpretation)
        assertEquals(AnalysisState.Complete, e.state)
        assertEquals(listOf("Left repeater"), e.cameras)
        assertEquals("North Las Vegas", e.location)
    }

    @Test
    fun severityMapsAllThreatLevels() {
        assertEquals(Severity.Urgent, severityOf("high"))
        assertEquals(Severity.Attention, severityOf("medium"))
        assertEquals(Severity.Routine, severityOf("low"))
        assertEquals(Severity.Routine, severityOf("none"))
        assertNull(severityOf(""))
        assertNull(severityOf("bogus"))
    }

    @Test
    fun pendingBeforeFinalizeIsUploading() {
        val e = mapEvent(row(analysisState = "pending", completedAt = null, analysisJson = ""), today)
        assertEquals(AnalysisState.Uploading, e.state)
    }

    @Test
    fun pendingAfterFinalizeIsAnalyzing() {
        val e = mapEvent(row(analysisState = "pending", completedAt = "2026-07-15T09:00:30Z"), today)
        assertEquals(AnalysisState.Analyzing, e.state)
    }

    @Test
    fun analysisStatesMap() {
        assertEquals(AnalysisState.Failed, mapEvent(row(analysisState = "failed"), today).state)
        assertEquals(AnalysisState.Analyzing, mapEvent(row(analysisState = "running"), today).state)
        assertEquals(
            AnalysisState.Unsupported,
            mapEvent(row(analysisState = "skipped"), today).state,
        )
        assertEquals(
            AnalysisState.Encrypted,
            mapEvent(row(analysisState = "skipped", analysisError = "clip is encrypted"), today).state,
        )
    }

    @Test
    fun fallsBackToReasonTitleWhenNoVerdict() {
        val e = mapEvent(row(analysisJson = ""), today)
        assertEquals("Object detected nearby", e.title)
        assertNull(e.severity)
        assertNull(e.aiSummaryShort)
    }

    @Test
    fun blankCityBecomesUnknown() {
        val e = mapEvent(row(city = ""), today)
        assertEquals("Unknown location", e.location)
    }

    @Test
    fun dayLabelsRelativeToToday() {
        assertEquals("Today", dayLabel(LocalDate(2026, 7, 15), today))
        assertEquals("Yesterday", dayLabel(LocalDate(2026, 7, 14), today))
        assertEquals("Jul 4", dayLabel(LocalDate(2026, 7, 4), today))
    }

    @Test
    fun timeLabelFormatsTwelveHour() {
        assertEquals("9:00 AM", mapEvent(row(eventTs = "2026-07-15T09:00:00"), today).time)
        assertEquals("1:05 PM", mapEvent(row(eventTs = "2026-07-15T13:05:00"), today).time)
        assertEquals("12:00 AM", mapEvent(row(eventTs = "2026-07-15T00:00:00"), today).time)
        assertEquals("12:30 PM", mapEvent(row(eventTs = "2026-07-15T12:30:00"), today).time)
    }

    @Test
    fun malformedVerdictYieldsNoCrash() {
        val e = mapEvent(row(analysisJson = "{not json", threatLevel = "medium"), today)
        // Falls back to the threat_level column when the verdict blob won't parse.
        assertEquals(Severity.Attention, e.severity)
        assertEquals("Object detected nearby", e.title)
    }

    @Test
    fun groupsAreNewestFirstAndDayBucketed() {
        val rows = listOf(
            row(id = "a", firstSeen = "2026-07-15T09:00:00Z", eventTs = "2026-07-15T09:00:00"),
            row(id = "b", firstSeen = "2026-07-14T22:00:00Z", eventTs = "2026-07-14T22:00:00"),
            row(id = "c", firstSeen = "2026-07-15T18:00:00Z", eventTs = "2026-07-15T18:00:00"),
        )
        val groups = buildFeedGroups(rows, emptyMap(), today)
        assertEquals(listOf("Today", "Yesterday"), groups.map { it.label })
        // Within Today, newest (c @ 18:00) precedes older (a @ 09:00).
        assertEquals(listOf("c", "a"), groups.first().events.map { it.event.id })
    }

    @Test
    fun userMetaJoinsOntoEvents() {
        val meta = mapOf("a" to EventUserMeta(favorite = true, reviewed = true))
        val groups = buildFeedGroups(listOf(row(id = "a")), meta, today)
        val ewm = groups.first().events.first()
        assertTrue(ewm.meta.favorite)
        assertTrue(ewm.meta.reviewed)
    }
}

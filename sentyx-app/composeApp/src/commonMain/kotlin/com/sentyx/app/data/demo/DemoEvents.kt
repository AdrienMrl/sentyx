package com.sentyx.app.data.demo

import com.sentyx.app.domain.model.AnalysisState
import com.sentyx.app.domain.model.Moment
import com.sentyx.app.domain.model.SentryEvent
import com.sentyx.app.domain.model.Severity

/**
 * The demo Sentry events (e1–e9), ported faithfully from the design prototype's
 * `EVENTS` array (design/Sentyx.dc.html). Feed order within a day matches the
 * source array. `retentionOnCar` is set here ("3 days left" for completed
 * events, null otherwise); `retentionCloud` is plan-dependent and injected by
 * [DemoEventRepository] when a single event is observed.
 */
object DemoEvents {

    private val e5 = SentryEvent(
        id = "e5",
        dayLabel = "Today",
        time = "2:41 PM",
        location = "Downtown Garage",
        severity = Severity.Attention,
        title = "Motion at rear bumper",
        cameras = listOf("Rear"),
        durationLabel = "—",
        state = AnalysisState.Writing,
    )

    private val e1 = SentryEvent(
        id = "e1",
        dayLabel = "Today",
        time = "2:14 PM",
        location = "Downtown Garage",
        severity = Severity.Attention,
        title = "Person lingered near driver door",
        cameras = listOf("Left repeater", "Front"),
        durationLabel = "0:48",
        state = AnalysisState.Complete,
        confidencePct = 92,
        aiSummaryShort = "Looked through the driver window, then walked away — no contact.",
        description = "A person approached and looked through the driver window for several seconds, then walked away. No contact with the vehicle was detected.",
        facts = listOf(
            "1 person detected",
            "Approached driver-side door",
            "No contact with vehicle",
            "Duration 48s",
        ),
        interpretation = "Likely curiosity; behavior did not escalate and no attempt to open the vehicle was seen.",
        subjects = listOf("Person"),
        contactDetected = false,
        moments = listOf(
            Moment("0:04", "Person enters frame", 8),
            Moment("0:12", "Approaches driver door", 25),
            Moment("0:21", "Looks through window", 44),
            Moment("0:39", "Walks away", 81),
        ),
        retentionOnCar = "3 days left",
    )

    private val e6 = SentryEvent(
        id = "e6",
        dayLabel = "Today",
        time = "12:20 PM",
        location = "Location unknown",
        severity = Severity.Routine,
        title = "Event awaiting upload",
        cameras = listOf("Front"),
        durationLabel = "0:22",
        state = AnalysisState.Waiting,
    )

    private val e2 = SentryEvent(
        id = "e2",
        dayLabel = "Today",
        time = "11:38 AM",
        location = "Whole Foods · Rampart",
        severity = Severity.Routine,
        title = "Shopping cart passed close to vehicle",
        cameras = listOf("Front"),
        durationLabel = "1:02",
        state = AnalysisState.Complete,
        confidencePct = 88,
        aiSummaryShort = "Cart passed close by — no contact detected.",
        description = "A shopping cart passed close to the front of the vehicle while being pushed through the lot. No contact was detected.",
        facts = listOf(
            "1 cart detected",
            "Passed front of vehicle",
            "No contact",
            "Duration 1:02",
        ),
        interpretation = "Routine parking-lot activity; no risk indicators present.",
        subjects = listOf("Object · cart"),
        contactDetected = false,
        moments = listOf(
            Moment("0:09", "Cart enters frame", 15),
            Moment("0:31", "Closest approach", 50),
            Moment("0:52", "Cart moves away", 84),
        ),
        retentionOnCar = "3 days left",
    )

    private val e7 = SentryEvent(
        id = "e7",
        dayLabel = "Today",
        time = "9:52 AM",
        location = "Home",
        severity = null,
        title = "Encrypted event",
        cameras = listOf("Right repeater"),
        durationLabel = "0:15",
        state = AnalysisState.Encrypted,
    )

    private val e3 = SentryEvent(
        id = "e3",
        dayLabel = "Today",
        time = "8:05 AM",
        location = "Home",
        severity = Severity.Routine,
        title = "Pedestrian walked past vehicle",
        cameras = listOf("Right repeater"),
        durationLabel = "0:36",
        state = AnalysisState.Complete,
        confidencePct = 96,
        aiSummaryShort = "Walked past without stopping — no contact.",
        description = "A pedestrian walked past the vehicle without stopping. No stop or contact was detected.",
        facts = listOf(
            "1 person detected",
            "Walked past vehicle",
            "No stop, no contact",
            "Duration 36s",
        ),
        interpretation = "Passer-by on the sidewalk; no interaction with the vehicle.",
        subjects = listOf("Person"),
        contactDetected = false,
        moments = listOf(
            Moment("0:06", "Pedestrian enters frame", 17),
            Moment("0:18", "Passes vehicle", 50),
            Moment("0:29", "Exits frame", 81),
        ),
        retentionOnCar = "3 days left",
    )

    private val e8 = SentryEvent(
        id = "e8",
        dayLabel = "Today",
        time = "7:10 AM",
        location = "Home",
        severity = Severity.Routine,
        title = "Analysis could not complete",
        cameras = listOf("Front"),
        durationLabel = "0:19",
        state = AnalysisState.Failed,
    )

    private val e4 = SentryEvent(
        id = "e4",
        dayLabel = "Yesterday",
        time = "10:42 PM",
        location = "Street parking · 4th Ave",
        severity = Severity.Urgent,
        title = "Vehicle door made contact with passenger side",
        cameras = listOf("Right repeater", "Rear"),
        durationLabel = "1:14",
        state = AnalysisState.Complete,
        confidencePct = 95,
        aiSummaryShort = "Adjacent car door contacted the passenger side. Contact detected.",
        description = "An adjacent vehicle opened its door into the passenger side of the vehicle, making contact. The other party left shortly after.",
        facts = listOf(
            "1 vehicle, 1 person detected",
            "Door opened into passenger side",
            "Contact detected",
            "Duration 1:14",
        ),
        interpretation = "Possible door-ding incident. Review recommended; consider saving the original clip.",
        subjects = listOf("Vehicle", "Person"),
        contactDetected = true,
        moments = listOf(
            Moment("0:11", "Adjacent car arrives", 15),
            Moment("0:33", "Door opens", 44),
            Moment("0:36", "Contact with panel", 49),
            Moment("1:02", "Party departs", 84),
        ),
        retentionOnCar = "3 days left",
    )

    /** e9 — appended by [DemoEventRepository.loadEarlier] as the "Jul 12" group. */
    val earlier = SentryEvent(
        id = "e9",
        dayLabel = "Jul 12",
        time = "3:20 PM",
        location = "Office lot",
        severity = Severity.Routine,
        title = "Person walked between cars",
        cameras = listOf("Front"),
        durationLabel = "0:28",
        state = AnalysisState.Complete,
        confidencePct = 90,
        aiSummaryShort = "Walked between parked cars — no contact.",
        description = "A person walked between parked cars and continued past.",
        facts = listOf(
            "1 person detected",
            "No contact",
        ),
        interpretation = "Routine foot traffic.",
        subjects = listOf("Person"),
        contactDetected = false,
        moments = listOf(
            Moment("0:05", "Enters frame", 18),
            Moment("0:20", "Exits frame", 71),
        ),
        retentionOnCar = "3 days left",
    )

    /** Today + Yesterday, in feed order. */
    val base: List<SentryEvent> = listOf(e5, e1, e6, e2, e7, e3, e8, e4)
}

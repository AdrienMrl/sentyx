package com.sentyx.app.data.demo

import com.sentyx.app.domain.model.Plan
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/** Demo scenario for the paired device (drives the "Prototype states" screen). */
enum class DeviceScenario(val label: String) {
    Normal("Normal"),
    Offline("Offline"),
    LowStorage("Low storage"),
    Overheating("Overheating"),
    WeakPower("Weak power"),
    Backlog("Upload backlog"),
    FirmwareUpdate("FW update"),
    BackendDown("Backend down"),
}

/** Demo scenario for the events feed. */
enum class FeedScenario(val label: String) {
    Normal("Normal"),
    Empty("Empty"),
    Loading("Loading"),
    Offline("Offline"),
    Error("Server error"),
}

/**
 * Central switchboard for demo conditions. Demo repositories derive their
 * state from these flows; the Prototype States screen mutates them.
 * Real (Pi/backend) implementations simply won't depend on this class.
 */
class DemoStateController {
    val deviceScenario = MutableStateFlow(DeviceScenario.Normal)
    val feedScenario = MutableStateFlow(FeedScenario.Normal)
    val plan = MutableStateFlow(Plan.Free)

    private val _urgentEventTrigger = MutableStateFlow(0)
    /** Increments each time "Simulate incoming urgent event" is pressed. */
    val urgentEventTrigger: StateFlow<Int> = _urgentEventTrigger

    fun triggerUrgentEvent() {
        _urgentEventTrigger.value++
    }
}

package com.sentyx.app.feature.settings

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.core.ui.ToastData
import com.sentyx.app.core.ui.ToastTone
import com.sentyx.app.data.demo.DemoStateController
import com.sentyx.app.data.demo.DeviceScenario
import com.sentyx.app.data.demo.FeedScenario
import com.sentyx.app.domain.model.Plan
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.stateIn

/** Selected demo conditions shown as highlighted chips. */
data class PrototypeStatesUiState(
    val device: DeviceScenario,
    val feed: FeedScenario,
    val plan: Plan,
)

/**
 * Backs [PrototypeStatesScreen]. Reads/writes the [DemoStateController]
 * switchboard: device scenario, feed scenario, and plan chips apply live across
 * the app, and "Simulate incoming urgent event" bumps the trigger and shows a
 * tappable urgent toast.
 */
class PrototypeStatesViewModel(
    private val demoState: DemoStateController,
    private val toasts: ToastController,
) : ViewModel() {

    val state: StateFlow<PrototypeStatesUiState> =
        combine(
            demoState.deviceScenario,
            demoState.feedScenario,
            demoState.plan,
        ) { device, feed, plan ->
            PrototypeStatesUiState(device, feed, plan)
        }.stateIn(
            viewModelScope,
            SharingStarted.WhileSubscribed(5_000),
            PrototypeStatesUiState(
                device = demoState.deviceScenario.value,
                feed = demoState.feedScenario.value,
                plan = demoState.plan.value,
            ),
        )

    val deviceScenarios: List<DeviceScenario> = DeviceScenario.entries
    val feedScenarios: List<FeedScenario> = FeedScenario.entries
    val plans: List<Plan> = listOf(Plan.Free, Plan.Premium, Plan.Fleet)

    fun selectDevice(scenario: DeviceScenario) {
        demoState.deviceScenario.value = scenario
    }

    fun selectFeed(scenario: FeedScenario) {
        demoState.feedScenario.value = scenario
    }

    fun selectPlan(plan: Plan) {
        demoState.plan.value = plan
    }

    /**
     * Fires a simulated urgent event: bumps the demo trigger and shows a toast
     * whose "View" CTA opens event `e4` via [onOpenEvent].
     */
    fun triggerUrgent(onOpenEvent: (String) -> Unit) {
        demoState.triggerUrgentEvent()
        toasts.show(
            ToastData(
                title = "Urgent · Door contact detected",
                subtitle = "Just now · 4th Ave",
                tone = ToastTone.Urgent,
                cta = "View",
                onTap = { onOpenEvent("e4") },
            ),
        )
    }
}

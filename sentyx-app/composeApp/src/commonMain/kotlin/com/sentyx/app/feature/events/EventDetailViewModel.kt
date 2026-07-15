package com.sentyx.app.feature.events

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.core.ui.ToastData
import com.sentyx.app.core.ui.ToastTone
import com.sentyx.app.domain.model.AnalysisFeedback
import com.sentyx.app.domain.model.AnalysisState
import com.sentyx.app.domain.model.DeviceSnapshot
import com.sentyx.app.domain.model.EventWithMeta
import com.sentyx.app.domain.model.Severity
import com.sentyx.app.domain.model.TransferRoute
import com.sentyx.app.domain.repository.DeviceRepository
import com.sentyx.app.domain.repository.EventRepository
import com.sentyx.app.domain.repository.TransferRepository
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/**
 * Detail screen state for a single event: the observed event + meta, the active
 * camera and seek position (driven by "meaningful moment" jumps), and the
 * severity / download-route bottom-sheet visibility. Ports the design
 * prototype's `dt` assembly, sheets and `startDownload` (design/Sentyx.dc.html).
 */
data class EventDetailUiState(
    /** Null while loading or if the id is unknown. */
    val event: EventWithMeta? = null,
    val device: DeviceSnapshot? = null,
    val camIndex: Int = 0,
    /** Selected moment position (0..100); doubles as the seek fill and row highlight key. */
    val seekMoment: Int? = null,
    val severitySheet: Boolean = false,
    val routeSheet: Boolean = false,
    /** Route currently chosen inside the download sheet. */
    val selectedRoute: TransferRoute = TransferRoute.WifiDirect,
) {
    val seekPct: Int get() = seekMoment ?: 0
}

private data class DetailLocalUi(
    val camIndex: Int = 0,
    val seekMoment: Int? = null,
    val severitySheet: Boolean = false,
    val routeSheet: Boolean = false,
    val selectedRoute: TransferRoute = TransferRoute.WifiDirect,
)

class EventDetailViewModel(
    private val eventId: String,
    private val events: EventRepository,
    private val transfers: TransferRepository,
    private val device: DeviceRepository,
    private val toasts: ToastController,
) : ViewModel() {

    private val local = MutableStateFlow(DetailLocalUi())

    val state: StateFlow<EventDetailUiState> =
        combine(events.observeEvent(eventId), device.device, local) { event, dev, ui ->
            EventDetailUiState(
                event = event,
                device = dev,
                camIndex = ui.camIndex,
                seekMoment = ui.seekMoment,
                severitySheet = ui.severitySheet,
                routeSheet = ui.routeSheet,
                selectedRoute = ui.selectedRoute,
            )
        }.stateIn(viewModelScope, SharingStarted.WhileSubscribed(5_000), EventDetailUiState())

    /** Which routes are usable right now (Bluetooth is gated on BLE being connected). */
    fun availableRoutes(): List<Pair<TransferRoute, Boolean>> = transfers.availableRoutes()

    fun selectCamera(index: Int) = local.update { it.copy(camIndex = index) }

    fun jumpToMoment(positionPct: Int) = local.update { it.copy(seekMoment = positionPct) }

    fun showSeveritySheet() = local.update { it.copy(severitySheet = true) }
    fun dismissSeveritySheet() = local.update { it.copy(severitySheet = false) }

    fun pickSeverity(severity: Severity) {
        viewModelScope.launch { events.overrideSeverity(eventId, severity) }
        dismissSeveritySheet()
    }

    fun showRouteSheet() = local.update { it.copy(routeSheet = true) }
    fun dismissRouteSheet() = local.update { it.copy(routeSheet = false) }

    fun selectRoute(route: TransferRoute) = local.update { it.copy(selectedRoute = route) }

    fun toggleReviewed() {
        val current = state.value.event?.meta?.reviewed ?: return
        viewModelScope.launch { events.setReviewed(eventId, !current) }
    }

    fun toggleFavorite() {
        val current = state.value.event?.meta?.favorite ?: return
        viewModelScope.launch { events.setFavorite(eventId, !current) }
    }

    fun submitFeedback(feedback: AnalysisFeedback) {
        viewModelScope.launch { events.submitFeedback(eventId, feedback) }
        toasts.show(
            ToastData(
                title = "Thanks for the feedback",
                subtitle = "It helps tune future analysis",
                tone = ToastTone.Success,
            ),
        )
    }

    fun retry() {
        val event = state.value.event?.event ?: return
        if (event.state != AnalysisState.Failed) return
        viewModelScope.launch { events.retryAnalysis(eventId) }
        toasts.show(
            ToastData(
                title = "Retrying analysis…",
                subtitle = "Queued with Gemini",
                tone = ToastTone.Info,
            ),
        )
    }

    fun share() = toasts.show(
        ToastData("Share sheet", "Mock — no real share", ToastTone.Info),
    )

    fun requestDelete() = toasts.show(
        ToastData("Delete event?", "Mock — not deleted", ToastTone.Urgent),
    )

    fun fullscreen() = toasts.show(
        ToastData("Full-screen playback", "Mock player", ToastTone.Info),
    )

    fun extendRetention() = toasts.show(
        ToastData("Retention extended", "Kept for 30 days", ToastTone.Success),
    )

    /**
     * Start a download of the original clip over the chosen route. Shows the
     * "Download started" toast (its CTA invokes [onView]); navigation to the
     * transfers screen is the caller's responsibility.
     */
    fun startDownload(onView: () -> Unit) {
        val event = state.value.event?.event ?: return
        val route = state.value.selectedRoute
        viewModelScope.launch {
            transfers.start(
                eventId = event.id,
                title = event.title,
                sizeLabel = event.originalSizeLabel,
                route = route,
            )
        }
        dismissRouteSheet()
        toasts.show(
            ToastData(
                title = "Download started",
                subtitle = "${event.originalSizeLabel} over ${route.label}",
                tone = ToastTone.Info,
                cta = "View",
                onTap = onView,
            ),
        )
    }
}

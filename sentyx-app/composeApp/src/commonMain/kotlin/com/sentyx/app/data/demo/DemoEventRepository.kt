package com.sentyx.app.data.demo

import com.sentyx.app.domain.model.AnalysisFeedback
import com.sentyx.app.domain.model.AnalysisState
import com.sentyx.app.domain.model.EventUserMeta
import com.sentyx.app.domain.model.EventWithMeta
import com.sentyx.app.domain.model.FeedGroup
import com.sentyx.app.domain.model.FeedState
import com.sentyx.app.domain.model.Plan
import com.sentyx.app.domain.model.SentryEvent
import com.sentyx.app.domain.model.Severity
import com.sentyx.app.domain.repository.EventRepository
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/**
 * In-memory [EventRepository] backed by [DemoEvents]. The feed content state is
 * driven by [DemoStateController.feedScenario]; per-event mutations
 * (favorite/reviewed/severity/feedback) live in a separate meta map so the
 * immutable analysis output is never edited in place.
 */
class DemoEventRepository(
    private val scope: CoroutineScope,
    private val demoState: DemoStateController,
) : EventRepository {

    private val _events = MutableStateFlow(DemoEvents.base)
    private val _meta = MutableStateFlow<Map<String, EventUserMeta>>(emptyMap())
    private val _loadedEarlier = MutableStateFlow(false)

    override val feed: StateFlow<FeedState> =
        combine(
            _events,
            _meta,
            demoState.feedScenario,
            _loadedEarlier,
        ) { events, meta, scenario, loadedEarlier ->
            when (scenario) {
                FeedScenario.Empty -> FeedState.Empty
                FeedScenario.Loading -> FeedState.Loading
                FeedScenario.Offline -> FeedState.Offline
                FeedScenario.Error -> FeedState.Error
                FeedScenario.Normal -> {
                    val visible = if (loadedEarlier) events + DemoEvents.earlier else events
                    FeedState.Loaded(buildGroups(visible, meta))
                }
            }
        }.stateIn(scope, SharingStarted.Eagerly, FeedState.Loading)

    private fun buildGroups(
        events: List<SentryEvent>,
        meta: Map<String, EventUserMeta>,
    ): List<FeedGroup> {
        // Preserve first-seen day order: Today, then Yesterday, then older.
        val order = LinkedHashMap<String, MutableList<EventWithMeta>>()
        for (e in events) {
            order.getOrPut(e.dayLabel) { mutableListOf() }
                .add(EventWithMeta(e, meta[e.id] ?: EventUserMeta()))
        }
        return order.map { (label, items) -> FeedGroup(label, items) }
    }

    override fun observeEvent(id: String): Flow<EventWithMeta?> =
        combine(_events, _meta, demoState.plan) { events, meta, plan ->
            val event = (events + DemoEvents.earlier).firstOrNull { it.id == id }
                ?: return@combine null
            EventWithMeta(withCloudRetention(event, plan), meta[id] ?: EventUserMeta())
        }

    /** Cloud retention depends on the active plan (Free: 7 days, otherwise 30). */
    private fun withCloudRetention(event: SentryEvent, plan: Plan): SentryEvent {
        val cloud = if (plan == Plan.Free) "7 days left" else "30 days left"
        return event.copy(retentionCloud = cloud)
    }

    override suspend fun loadEarlier() {
        _loadedEarlier.value = true
    }

    override suspend fun setFavorite(id: String, favorite: Boolean) =
        patchMeta(id) { it.copy(favorite = favorite) }

    override suspend fun setReviewed(id: String, reviewed: Boolean) =
        patchMeta(id) { it.copy(reviewed = reviewed) }

    override suspend fun overrideSeverity(id: String, severity: Severity) =
        patchMeta(id) { it.copy(severityOverride = severity) }

    override suspend fun submitFeedback(id: String, feedback: AnalysisFeedback) =
        patchMeta(id) { it.copy(feedback = feedback) }

    private inline fun patchMeta(id: String, transform: (EventUserMeta) -> EventUserMeta) {
        _meta.update { current ->
            current + (id to transform(current[id] ?: EventUserMeta()))
        }
    }

    override suspend fun retryAnalysis(id: String) {
        val target = _events.value.firstOrNull { it.id == id } ?: return
        if (target.state != AnalysisState.Failed) return
        setState(id, AnalysisState.Analyzing)
        scope.launch {
            delay(3000)
            setState(id, AnalysisState.Failed)
        }
    }

    private fun setState(id: String, state: AnalysisState) {
        _events.update { list -> list.map { if (it.id == id) it.copy(state = state) else it } }
    }

    override suspend fun delete(ids: Set<String>) {
        if ("e9" in ids) _loadedEarlier.value = false
        _events.update { list -> list.filterNot { it.id in ids } }
        _meta.update { current -> current - ids }
    }
}

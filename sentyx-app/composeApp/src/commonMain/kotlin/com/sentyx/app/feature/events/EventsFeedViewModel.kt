package com.sentyx.app.feature.events

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.core.ui.ToastData
import com.sentyx.app.data.thumbnail.EventThumbnailLoader
import com.sentyx.app.core.ui.ToastTone
import com.sentyx.app.domain.model.DeviceSnapshot
import com.sentyx.app.domain.model.EventWithMeta
import com.sentyx.app.domain.model.FeedFilter
import com.sentyx.app.domain.model.FeedGroup
import com.sentyx.app.domain.model.FeedState
import com.sentyx.app.domain.model.Severity
import com.sentyx.app.domain.repository.DeviceRepository
import com.sentyx.app.domain.repository.EventRepository
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/**
 * Feed screen state: the (filtered) grouped feed, the car status tile source,
 * the active severity/favorites filter, plus select-mode and search-toggle UI.
 * Ports the design prototype's feed logic (`enrich`, filter, select mode, bulk
 * actions) from design/Sentyx.dc.html.
 */
data class EventsFeedUiState(
    val feed: FeedState = FeedState.Loading,
    /** Groups after applying [filter]; empty unless [feed] is [FeedState.Loaded]. */
    val displayGroups: List<FeedGroup> = emptyList(),
    val device: DeviceSnapshot? = null,
    val filter: FeedFilter = FeedFilter.All,
    val selectMode: Boolean = false,
    val selected: Set<String> = emptySet(),
    val searchOpen: Boolean = false,
    /** Count of events in the "Today" group (unfiltered), for the car tile. */
    val todayCount: Int = 0,
) {
    val selectBarVisible: Boolean get() = selectMode && selected.isNotEmpty()
    val selectedCount: Int get() = selected.size
    val selectLabel: String get() = if (selectMode) "Done" else "Select"
}

private data class FeedLocalUi(
    val filter: FeedFilter = FeedFilter.All,
    val selectMode: Boolean = false,
    val selected: Set<String> = emptySet(),
    val searchOpen: Boolean = false,
)

class EventsFeedViewModel(
    private val events: EventRepository,
    private val device: DeviceRepository,
    private val toasts: ToastController,
    /** Feed cards fetch/decode their thumbnails through this; no-op in demo builds. */
    val thumbnails: EventThumbnailLoader,
) : ViewModel() {

    private val local = MutableStateFlow(FeedLocalUi())

    val state: StateFlow<EventsFeedUiState> =
        combine(events.feed, device.device, local) { feed, dev, ui ->
            val groups = (feed as? FeedState.Loaded)?.groups ?: emptyList()
            EventsFeedUiState(
                feed = feed,
                displayGroups = filterGroups(groups, ui.filter),
                device = dev,
                filter = ui.filter,
                selectMode = ui.selectMode,
                selected = ui.selected,
                searchOpen = ui.searchOpen,
                todayCount = groups.firstOrNull { it.label == TODAY_LABEL }?.events?.size ?: 0,
            )
        }.stateIn(viewModelScope, SharingStarted.WhileSubscribed(5_000), EventsFeedUiState())

    private fun filterGroups(groups: List<FeedGroup>, filter: FeedFilter): List<FeedGroup> =
        groups.mapNotNull { group ->
            val items = group.events.filter { matches(it, filter) }
            if (items.isEmpty()) null else FeedGroup(group.label, items)
        }

    private fun matches(item: EventWithMeta, filter: FeedFilter): Boolean = when (filter) {
        FeedFilter.All -> true
        FeedFilter.Favorites -> item.meta.favorite
        FeedFilter.Urgent -> item.severity == Severity.Urgent
        FeedFilter.Attention -> item.severity == Severity.Attention
        FeedFilter.Routine -> item.severity == Severity.Routine
    }

    fun setFilter(filter: FeedFilter) = local.update { it.copy(filter = filter) }

    fun toggleSelectMode() =
        local.update { it.copy(selectMode = !it.selectMode, selected = emptySet()) }

    fun toggleSearch() = local.update { it.copy(searchOpen = !it.searchOpen) }

    /** Toggle a card's membership in the selection (used when a card is tapped in select mode). */
    fun toggleSelection(id: String) = local.update {
        it.copy(selected = if (id in it.selected) it.selected - id else it.selected + id)
    }

    fun loadEarlier() {
        viewModelScope.launch { events.loadEarlier() }
    }

    /** Bulk "Download": exit select mode and confirm via toast. Navigation is the caller's. */
    fun bulkDownload() {
        val ids = local.value.selected
        if (ids.isEmpty()) return
        exitSelect()
        toasts.show(
            ToastData(
                title = "Added to transfers",
                subtitle = "Selected clips queued",
                tone = ToastTone.Info,
                cta = "View",
            ),
        )
    }

    /** Bulk "Reviewed": mark every selected event reviewed via the repo, then exit select mode. */
    fun bulkReviewed() {
        val ids = local.value.selected
        if (ids.isEmpty()) return
        viewModelScope.launch {
            ids.forEach { events.setReviewed(it, true) }
        }
        exitSelect()
    }

    /** Bulk "Delete": design is a mock — nothing is removed. */
    fun bulkDelete() {
        if (local.value.selected.isEmpty()) return
        exitSelect()
        toasts.show(
            ToastData(
                title = "Events deleted",
                subtitle = "Mock — nothing removed",
                tone = ToastTone.Urgent,
            ),
        )
    }

    private fun exitSelect() = local.update { it.copy(selectMode = false, selected = emptySet()) }

    private companion object {
        const val TODAY_LABEL = "Today"
    }
}

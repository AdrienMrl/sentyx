package com.sentyx.app.data.event

import com.sentyx.app.core.storage.KeyValueStore
import com.sentyx.app.core.storage.StorageKeys
import com.sentyx.app.data.api.EventSummaryDto
import com.sentyx.app.data.api.SentyxApi
import com.sentyx.app.domain.model.AnalysisFeedback
import com.sentyx.app.domain.model.EventUserMeta
import com.sentyx.app.domain.model.EventWithMeta
import com.sentyx.app.domain.model.FeedState
import com.sentyx.app.domain.model.Severity
import kotlinx.coroutines.CancellationException
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
import com.sentyx.app.core.platform.currentEpochMillis
import kotlinx.datetime.Instant
import kotlinx.datetime.LocalDate
import kotlinx.datetime.TimeZone
import kotlinx.datetime.toLocalDateTime
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import com.sentyx.app.domain.repository.EventRepository

/**
 * Real [EventRepository] backed by the Sentyx backend. Polls `GET /events`
 * every [POLL_INTERVAL_MS], maps rows onto the day-grouped feed via the pure
 * [buildFeedGroups] mapping, and layers per-event user annotations that live
 * only on-device (the server exposes no user-meta endpoints yet), persisted as a
 * JSON blob in [KeyValueStore].
 *
 * Pagination is client-side: the server returns the full list, so [loadEarlier]
 * simply widens a visible-count window over the newest-first rows.
 *
 * Mutations that need a server endpoint we don't have yet — [retryAnalysis] and
 * [delete] — are deliberate no-ops (the UI still shows its optimistic toast).
 */
class RealEventRepository(
    private val scope: CoroutineScope,
    private val api: SentyxApi,
    private val store: KeyValueStore,
) : EventRepository {

    /** null until the first poll resolves (distinguishes Loading from Empty). */
    private val _rows = MutableStateFlow<List<EventSummaryDto>?>(null)

    /** False after a poll failure, so the UI can show Offline instead of a live feed. */
    private val _reachable = MutableStateFlow(true)

    private val _meta = MutableStateFlow(loadMeta())
    private val _visibleLimit = MutableStateFlow(PAGE_SIZE)

    override val feed: StateFlow<FeedState> =
        combine(_rows, _meta, _visibleLimit, _reachable) { rows, meta, limit, reachable ->
            when {
                rows == null -> if (reachable) FeedState.Loading else FeedState.Offline
                rows.isEmpty() -> if (reachable) FeedState.Empty else FeedState.Offline
                else -> {
                    val visible = rows
                        .sortedByDescending { it.firstSeen ?: it.eventTs }
                        .take(limit)
                    FeedState.Loaded(buildFeedGroups(visible, meta, today()))
                }
            }
        }.stateIn(scope, SharingStarted.Eagerly, FeedState.Loading)

    init {
        scope.launch { pollLoop() }
    }

    private suspend fun pollLoop() {
        while (true) {
            try {
                _rows.value = api.events()
                _reachable.value = true
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                // Keep the last rows (if any) but flag the backend unreachable.
                _reachable.value = false
            }
            delay(POLL_INTERVAL_MS)
        }
    }

    override fun observeEvent(id: String): Flow<EventWithMeta?> =
        combine(_rows, _meta) { rows, meta ->
            val row = rows?.firstOrNull { it.id == id } ?: return@combine null
            EventWithMeta(mapEvent(row, today()), meta[id] ?: EventUserMeta())
        }

    override suspend fun loadEarlier() {
        _visibleLimit.update { it + PAGE_SIZE }
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
            val next = current + (id to transform(current[id] ?: EventUserMeta()))
            persistMeta(next)
            next
        }
    }

    override suspend fun retryAnalysis(id: String) {
        // No re-analyze endpoint on the server yet; the feed will reflect a real
        // state change once one exists. No-op so the UI's optimistic toast shows.
    }

    override suspend fun delete(ids: Set<String>) {
        // No delete endpoint on the server yet. No-op: hiding locally would just
        // reappear on the next poll, which is worse than a clear no-op.
    }

    // ---- User-meta persistence ----------------------------------------------

    private fun persistMeta(meta: Map<String, EventUserMeta>) {
        val dto = meta.mapValues { (_, m) ->
            StoredMeta(
                favorite = m.favorite,
                reviewed = m.reviewed,
                severityOverride = m.severityOverride?.name,
                feedback = m.feedback?.name,
            )
        }
        store.putString(StorageKeys.EVENT_META, metaCodec.encodeToString(dto))
    }

    private fun loadMeta(): Map<String, EventUserMeta> {
        val raw = store.getString(StorageKeys.EVENT_META) ?: return emptyMap()
        return try {
            metaCodec.decodeFromString<Map<String, StoredMeta>>(raw).mapValues { (_, s) ->
                EventUserMeta(
                    favorite = s.favorite,
                    reviewed = s.reviewed,
                    severityOverride = s.severityOverride?.let { Severity.valueOf(it) },
                    feedback = s.feedback?.let { AnalysisFeedback.valueOf(it) },
                )
            }
        } catch (e: Exception) {
            emptyMap()
        }
    }

    private fun today(): LocalDate =
        Instant.fromEpochMilliseconds(currentEpochMillis())
            .toLocalDateTime(TimeZone.currentSystemDefault()).date

    /** On-disk shape of [EventUserMeta]; enums stored by name so they survive renames of the JSON. */
    @Serializable
    private data class StoredMeta(
        val favorite: Boolean = false,
        val reviewed: Boolean = false,
        @SerialName("severity") val severityOverride: String? = null,
        val feedback: String? = null,
    )

    companion object {
        private const val POLL_INTERVAL_MS = 30_000L
        private const val PAGE_SIZE = 50
        private val metaCodec = Json { ignoreUnknownKeys = true }
    }
}

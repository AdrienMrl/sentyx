package com.sentyx.app.domain.repository

import com.sentyx.app.domain.model.AnalysisFeedback
import com.sentyx.app.domain.model.EventWithMeta
import com.sentyx.app.domain.model.FeedState
import com.sentyx.app.domain.model.Severity
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.StateFlow

/** Read + user-annotate Sentry events. Implementations: demo now, backend later. */
interface EventRepository {
    /** The full feed, already grouped by day, newest first. */
    val feed: StateFlow<FeedState>

    fun observeEvent(id: String): Flow<EventWithMeta?>

    /** Appends older groups to the feed (pagination). */
    suspend fun loadEarlier()

    suspend fun setFavorite(id: String, favorite: Boolean)
    suspend fun setReviewed(id: String, reviewed: Boolean)
    suspend fun overrideSeverity(id: String, severity: Severity)
    suspend fun submitFeedback(id: String, feedback: AnalysisFeedback)
    suspend fun retryAnalysis(id: String)
    suspend fun delete(ids: Set<String>)
}

package com.sentyx.app.data.clip

import com.sentyx.app.data.api.SentyxApi
import kotlinx.coroutines.CancellationException

/**
 * A playable video source: the fully-qualified clip URL plus the HTTP headers
 * (the bearer `Authorization`) the platform player must send on every request,
 * including the byte-range requests it issues while seeking. The header token is
 * captured once when the source is built; a token that expires mid-playback is
 * an accepted limitation (clips are short), not something we refresh here.
 */
data class ClipSource(val url: String, val headers: Map<String, String>)

/**
 * Builds a [ClipSource] for an event's clip, or returns null when none can be
 * produced (demo builds, or the user is signed out). Mirrors
 * [com.sentyx.app.data.thumbnail.EventThumbnailLoader]: the caller stays in the
 * placeholder video state when this yields null.
 */
interface EventClipLoader {
    suspend fun clipSource(eventId: String): ClipSource?
}

/**
 * No-op loader for demo builds, which have no real backend. Always returns null
 * so the event detail screen keeps its striped placeholder and never mounts a
 * platform player (matching [com.sentyx.app.data.thumbnail.NoopThumbnailLoader]).
 */
object NoopClipLoader : EventClipLoader {
    override suspend fun clipSource(eventId: String): ClipSource? = null
}

/**
 * Backend-backed loader. Delegates to [SentyxApi.clipSource], which composes the
 * `GET {base}/events/{id}/clip` URL and a currently-valid bearer header. A
 * signed-out session (or any other failure) yields null so the UI degrades to
 * the placeholder rather than surfacing an error.
 */
class SentyxClipLoader(private val api: SentyxApi) : EventClipLoader {
    override suspend fun clipSource(eventId: String): ClipSource? =
        try {
            api.clipSource(eventId)
        } catch (c: CancellationException) {
            throw c
        } catch (t: Throwable) {
            null
        }
}

package com.sentyx.app.data.thumbnail

import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.decodeToImageBitmap
import com.sentyx.app.core.cache.LruCache
import com.sentyx.app.data.api.SentyxApi
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext

/**
 * Loads event feed thumbnails. Returns the decoded bitmap for an event id, or
 * null when none is available yet (the backend 404s until a clip has been
 * uploaded and analyzed, and any other failure is treated the same way).
 */
interface EventThumbnailLoader {
    suspend fun load(eventId: String): ImageBitmap?
}

/**
 * No-op loader for demo builds, which have no real backend. Always returns null
 * so the feed card keeps its striped placeholder.
 */
object NoopThumbnailLoader : EventThumbnailLoader {
    override suspend fun load(eventId: String): ImageBitmap? = null
}

/**
 * Backend-backed loader. Fetches JPEG/PNG bytes via [SentyxApi], decodes them
 * off the main thread ([Dispatchers.Default]), and keeps the most recent
 * [MAX_ENTRIES] decoded bitmaps in memory. Concurrent loads for the same id
 * share one fetch (dedup). Misses and failures are never cached: a thumbnail
 * that 404s now can appear once analysis completes.
 */
class SentyxThumbnailLoader(private val api: SentyxApi) : EventThumbnailLoader {

    // [cache] and [inFlight] are both guarded by [lock]; LruCache is not thread-safe.
    private val cache = LruCache<ImageBitmap>(MAX_ENTRIES)
    private val inFlight = HashMap<String, CompletableDeferred<ImageBitmap?>>()
    private val lock = Mutex()

    override suspend fun load(eventId: String): ImageBitmap? {
        val deferred: CompletableDeferred<ImageBitmap?>
        val owner: Boolean
        lock.withLock {
            cache.get(eventId)?.let { return it }
            val existing = inFlight[eventId]
            if (existing != null) {
                deferred = existing
                owner = false
            } else {
                deferred = CompletableDeferred()
                inFlight[eventId] = deferred
                owner = true
            }
        }
        if (!owner) return deferred.await()

        // Owner: fetch + decode exactly once, publish to any joiners, and always
        // clear the in-flight slot even on cancellation so a later load retries.
        var decoded: ImageBitmap? = null
        try {
            val bytes = api.eventThumb(eventId)
            if (bytes != null) {
                decoded = withContext(Dispatchers.Default) { bytes.decodeToImageBitmap() }
            }
        } catch (c: CancellationException) {
            throw c
        } catch (t: Throwable) {
            decoded = null
        } finally {
            val result = decoded
            withContext(NonCancellable) {
                lock.withLock {
                    inFlight.remove(eventId)
                    if (result != null) cache.put(eventId, result)
                }
                deferred.complete(result)
            }
        }
        return decoded
    }

    private companion object {
        const val MAX_ENTRIES = 50
    }
}

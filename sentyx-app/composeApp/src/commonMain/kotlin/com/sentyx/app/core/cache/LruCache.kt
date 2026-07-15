package com.sentyx.app.core.cache

/**
 * Bounded, access-ordered LRU keyed by [String]. A hit ([get]) and a write
 * ([put]) both refresh recency, and inserting past [maxEntries] evicts the
 * least-recently-used entry. Not thread-safe — callers must guard concurrent
 * access (the thumbnail loader holds a mutex around it).
 */
class LruCache<V : Any>(private val maxEntries: Int) {
    init {
        require(maxEntries > 0) { "maxEntries must be positive, was $maxEntries" }
    }

    // LinkedHashMap preserves insertion order; remove-then-reinsert moves a key
    // to the most-recent (last) position, so keys.first() is always the LRU one.
    private val map = LinkedHashMap<String, V>()

    val size: Int get() = map.size

    fun get(key: String): V? {
        val value = map.remove(key) ?: return null
        map[key] = value
        return value
    }

    fun put(key: String, value: V) {
        map.remove(key)
        map[key] = value
        while (map.size > maxEntries) {
            map.remove(map.keys.first())
        }
    }
}

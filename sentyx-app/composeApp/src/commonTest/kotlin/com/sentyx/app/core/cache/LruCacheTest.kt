package com.sentyx.app.core.cache

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertNull

class LruCacheTest {

    @Test
    fun getMiss_returnsNull() {
        val cache = LruCache<String>(2)
        assertNull(cache.get("absent"))
    }

    @Test
    fun putThenGet_returnsValue() {
        val cache = LruCache<String>(2)
        cache.put("a", "A")
        assertEquals("A", cache.get("a"))
    }

    @Test
    fun evictsLeastRecentlyUsed_pastCapacity() {
        val cache = LruCache<String>(2)
        cache.put("a", "A")
        cache.put("b", "B")
        cache.put("c", "C") // evicts "a" (least recent)
        assertNull(cache.get("a"))
        assertEquals("B", cache.get("b"))
        assertEquals("C", cache.get("c"))
        assertEquals(2, cache.size)
    }

    @Test
    fun getRefreshesRecency_soThatKeySurvivesEviction() {
        val cache = LruCache<String>(2)
        cache.put("a", "A")
        cache.put("b", "B")
        cache.get("a")      // "a" now most-recent, "b" is LRU
        cache.put("c", "C") // evicts "b", not "a"
        assertEquals("A", cache.get("a"))
        assertNull(cache.get("b"))
        assertEquals("C", cache.get("c"))
    }

    @Test
    fun putSameKey_updatesValueWithoutGrowing() {
        val cache = LruCache<String>(2)
        cache.put("a", "A")
        cache.put("a", "A2")
        assertEquals("A2", cache.get("a"))
        assertEquals(1, cache.size)
    }

    @Test
    fun nonPositiveCapacity_throws() {
        assertFailsWith<IllegalArgumentException> { LruCache<String>(0) }
    }
}

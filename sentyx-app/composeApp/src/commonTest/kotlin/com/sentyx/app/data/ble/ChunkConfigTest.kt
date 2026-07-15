package com.sentyx.app.data.ble

import kotlin.test.Test
import kotlin.test.assertEquals

class ChunkConfigTest {

    private fun frames(payload: String, maxFrame: Int): List<List<Int>> =
        SentyxGatt.chunkConfig(payload.encodeToByteArray(), maxFrame)
            .map { frame -> frame.map { it.toInt() and 0xFF } }

    private fun expected(vararg frames: List<Any>): List<List<Int>> =
        frames.map { frame ->
            frame.map { token ->
                when (token) {
                    is Int -> token
                    is Char -> token.code
                    else -> error("unexpected token $token")
                }
            }
        }

    @Test
    fun singleFrame_fitsExactlyUnderLimit() {
        // "abc", maxFrame 5 -> [0x03 'a' 'b' 'c']
        assertEquals(
            expected(listOf(0x03, 'a', 'b', 'c')),
            frames("abc", 5),
        )
    }

    @Test
    fun singleFrame_fillsPayloadCapacity() {
        // "abcd", maxFrame 5 -> [0x03 'a' 'b' 'c' 'd']  (4 payload bytes == maxFrame-1)
        assertEquals(
            expected(listOf(0x03, 'a', 'b', 'c', 'd')),
            frames("abcd", 5),
        )
    }

    @Test
    fun twoFrames_firstAndLast() {
        // "abcde", maxFrame 5 -> [0x01 'a' 'b' 'c' 'd'], [0x02 'e']
        assertEquals(
            expected(
                listOf(0x01, 'a', 'b', 'c', 'd'),
                listOf(0x02, 'e'),
            ),
            frames("abcde", 5),
        )
    }

    @Test
    fun threeFrames_firstContinuationLast() {
        // "abcdefghij", maxFrame 5 -> [0x01 abcd], [0x00 efgh], [0x02 ij]
        assertEquals(
            expected(
                listOf(0x01, 'a', 'b', 'c', 'd'),
                listOf(0x00, 'e', 'f', 'g', 'h'),
                listOf(0x02, 'i', 'j'),
            ),
            frames("abcdefghij", 5),
        )
    }

    @Test
    fun emptyPayload_singleHeaderOnlyFrame() {
        // "", maxFrame 5 -> [0x03]
        assertEquals(
            expected(listOf(0x03)),
            frames("", 5),
        )
    }
}

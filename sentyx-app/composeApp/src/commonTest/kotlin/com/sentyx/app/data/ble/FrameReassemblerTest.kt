package com.sentyx.app.data.ble

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

class FrameReassemblerTest {

    /** Feed [payload]'s [SentyxGatt.chunkConfig] frames back through the reassembler. */
    private fun roundTrip(payload: String, maxFrame: Int): String? {
        val reassembler = SentyxGatt.FrameReassembler()
        var result: ByteArray? = null
        for (frame in SentyxGatt.chunkConfig(payload.encodeToByteArray(), maxFrame)) {
            reassembler.accept(frame)?.let { result = it }
        }
        return result?.decodeToString()
    }

    @Test
    fun singleFrame_roundTrips() {
        assertEquals("abc", roundTrip("abc", 5))
    }

    @Test
    fun payloadFillingOneFrame_roundTrips() {
        assertEquals("abcd", roundTrip("abcd", 5))
    }

    @Test
    fun twoFrames_roundTrip() {
        assertEquals("abcde", roundTrip("abcde", 5))
    }

    @Test
    fun manyFrames_roundTrip() {
        assertEquals("abcdefghij", roundTrip("abcdefghij", 5))
    }

    @Test
    fun emptyPayload_roundTrips() {
        assertEquals("", roundTrip("", 5))
    }

    @Test
    fun realisticJson_roundTrips() {
        val json = """{"v":1,"op":"status","ok":true,"current":{"ssid":"Garage 5G","signal":72}}"""
        assertEquals(json, roundTrip(json, 20))
    }

    @Test
    fun midStreamFrames_returnNullUntilComplete() {
        val reassembler = SentyxGatt.FrameReassembler()
        val frames = SentyxGatt.chunkConfig("abcdefghij".encodeToByteArray(), 5)
        // First and continuation frames yield nothing; only the last completes.
        assertNull(reassembler.accept(frames[0]))
        assertNull(reassembler.accept(frames[1]))
        assertEquals("abcdefghij", reassembler.accept(frames[2])?.decodeToString())
    }

    @Test
    fun firstFrameRestartsBuffer() {
        val reassembler = SentyxGatt.FrameReassembler()
        // A stale FIRST/CONTINUATION with no LAST, then a fresh complete sequence.
        val stale = SentyxGatt.chunkConfig("xxxxxxxxxx".encodeToByteArray(), 5)
        reassembler.accept(stale[0])
        reassembler.accept(stale[1])
        assertEquals("abcde", roundTripInto(reassembler, "abcde", 5))
    }

    private fun roundTripInto(reassembler: SentyxGatt.FrameReassembler, payload: String, maxFrame: Int): String? {
        var result: ByteArray? = null
        for (frame in SentyxGatt.chunkConfig(payload.encodeToByteArray(), maxFrame)) {
            reassembler.accept(frame)?.let { result = it }
        }
        return result?.decodeToString()
    }
}

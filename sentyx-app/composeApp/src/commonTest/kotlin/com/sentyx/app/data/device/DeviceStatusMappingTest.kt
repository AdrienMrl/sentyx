package com.sentyx.app.data.device

import com.sentyx.app.data.api.DeviceStatusDto
import com.sentyx.app.data.api.HeartbeatDto
import com.sentyx.app.domain.model.DeviceCondition
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

class DeviceStatusMappingTest {

    private val now = 1_752_600_300_000L

    private fun heartbeat(
        agentVersion: String? = "dev",
        uptimeSec: Long? = 3_600,
        storageFreeBytes: Long? = 21_000_000_000,
        storageTotalBytes: Long? = 32_000_000_000,
        cpuTempC: Double? = 52.3,
        underVoltageNow: Boolean? = false,
        underVoltageEver: Boolean? = false,
        wifiSsid: String? = "STARLINK",
        wifiRssiDbm: Int? = -55,
        wifiSignalPct: Int? = 78,
        uploadBacklog: Int? = 3,
        recordingNow: Boolean? = true,
        sentryActive: Boolean? = true,
    ) = HeartbeatDto(
        v = 1,
        agentVersion = agentVersion,
        uptimeSec = uptimeSec,
        storageFreeBytes = storageFreeBytes,
        storageTotalBytes = storageTotalBytes,
        cpuTempC = cpuTempC,
        underVoltageNow = underVoltageNow,
        underVoltageEver = underVoltageEver,
        wifiSsid = wifiSsid,
        wifiRssiDbm = wifiRssiDbm,
        wifiSignalPct = wifiSignalPct,
        uploadBacklog = uploadBacklog,
        recordingNow = recordingNow,
        sentryActive = sentryActive,
    )

    private fun status(online: Boolean, lastSeenMs: Long?, st: HeartbeatDto?) = DeviceStatusDto(
        deviceId = "raspberrypi",
        name = "Garage Pi",
        registeredAtMs = 1_752_600_000_000,
        online = online,
        lastSeenMs = lastSeenMs,
        status = st,
    )

    @Test
    fun healthyOnlineDevice() {
        val snap = mapDeviceSnapshot(status(true, now - 5_000, heartbeat()), "Garage Pi", now)

        assertEquals("raspberrypi", snap.id)
        assertEquals("Garage Pi", snap.name)
        assertEquals(DeviceCondition.Online, snap.condition)
        assertEquals("Online · Wi-Fi", snap.statusText)
        assertEquals("Watching", snap.sentryStatus)
        assertEquals(66, snap.storageFreePct) // 21/32 ≈ 65.6 → 66
        assertEquals("Healthy", snap.powerStatus)
        assertEquals("Normal", snap.temperatureStatus)
        assertEquals("dev · current", snap.firmwareVersion)
        assertFalse(snap.firmwareUpdateAvailable)
        assertEquals(3, snap.uploadBacklog)
        assertFalse(snap.bleConnected)
        assertTrue(snap.wifiConnected)
        assertEquals("STARLINK", snap.wifiNetwork)
        assertTrue(snap.backendConnected)
        assertEquals("Just now", snap.lastSeen)
        assertTrue(snap.recordingNow)
        assertEquals("Strong", snap.signalStrength)
        assertTrue(snap.issues.isEmpty())
    }

    @Test
    fun offlineDeviceWithLastSeenHumanized() {
        val snap = mapDeviceSnapshot(status(false, now - 21 * 60_000, heartbeat()), "Garage Pi", now)

        assertEquals(DeviceCondition.Offline, snap.condition)
        assertEquals("Offline", snap.statusText)
        assertFalse(snap.wifiConnected)
        assertFalse(snap.backendConnected)
        assertEquals("21m ago", snap.lastSeen)
        assertTrue(snap.issues.any { it.contains("offline") })
    }

    @Test
    fun lowStorageTriggersNeedsAttention() {
        val st = heartbeat(storageFreeBytes = 1_000_000_000, storageTotalBytes = 32_000_000_000)
        val snap = mapDeviceSnapshot(status(true, now, st), "Garage Pi", now)

        assertEquals(DeviceCondition.NeedsAttention, snap.condition)
        assertEquals(3, snap.storageFreePct)
        assertTrue(snap.issues.any { it.contains("Low storage") })
    }

    @Test
    fun underVoltageTriggersNeedsAttention() {
        val st = heartbeat(underVoltageNow = true)
        val snap = mapDeviceSnapshot(status(true, now, st), "Garage Pi", now)

        assertEquals(DeviceCondition.NeedsAttention, snap.condition)
        assertEquals("Under-voltage", snap.powerStatus)
        assertTrue(snap.issues.any { it.contains("Under-voltage") })
    }

    @Test
    fun highBacklogTriggersNeedsAttentionAndIssue() {
        val st = heartbeat(uploadBacklog = 12)
        val snap = mapDeviceSnapshot(status(true, now, st), "Garage Pi", now)

        assertEquals(DeviceCondition.NeedsAttention, snap.condition)
        assertTrue(snap.issues.any { it.contains("12 clips waiting to upload") })
    }

    @Test
    fun nullStatusMapsToHonestUnknowns() {
        val snap = mapDeviceSnapshot(status(false, null, null), "Garage Pi", now)

        assertEquals(DeviceCondition.Offline, snap.condition)
        assertEquals("Unknown", snap.sentryStatus)
        assertEquals("Unknown", snap.powerStatus)
        assertEquals("Unknown", snap.temperatureStatus)
        assertEquals("Unknown", snap.firmwareVersion)
        assertEquals("Unknown", snap.signalStrength)
        assertEquals("Never", snap.lastSeen)
        assertEquals(UNKNOWN_STORAGE_PCT, snap.storageFreePct)
    }

    @Test
    fun onlineWithoutSsidDropsWiFiSuffix() {
        val st = heartbeat(wifiSsid = null)
        val snap = mapDeviceSnapshot(status(true, now, st), "Garage Pi", now)

        assertEquals("Online", snap.statusText)
        assertEquals(null, snap.wifiNetwork)
    }

    @Test
    fun temperatureBands() {
        assertEquals("Warm", mapDeviceSnapshot(status(true, now, heartbeat(cpuTempC = 72.0)), "x", now).temperatureStatus)
        assertEquals("Hot", mapDeviceSnapshot(status(true, now, heartbeat(cpuTempC = 85.0)), "x", now).temperatureStatus)
    }

    @Test
    fun diagnosticsFromHeartbeat() {
        val diags = buildDiagnostics(heartbeat())
        val labels = diags.map { it.label }
        assertTrue("Uptime" in labels)
        assertTrue("CPU temperature" in labels)
        assertTrue("Wi-Fi signal" in labels)
        assertTrue("Upload backlog" in labels)
        assertTrue("Storage free" in labels)
        assertEquals("1h 0m", diags.first { it.label == "Uptime" }.value)
    }

    @Test
    fun diagnosticsEmptyWhenNoHeartbeat() {
        assertTrue(buildDiagnostics(null).isEmpty())
    }
}

package com.sentyx.app.data.demo

import com.sentyx.app.domain.model.ConnectionTestStep
import com.sentyx.app.domain.model.DeviceCondition
import com.sentyx.app.domain.model.DeviceSnapshot
import com.sentyx.app.domain.model.DiagnosticEntry
import com.sentyx.app.domain.model.FirmwareUpdate
import com.sentyx.app.domain.repository.DeviceRepository
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.stateIn

/**
 * Demo [DeviceRepository]. The 8 device conditions map from
 * [DemoStateController.deviceScenario] (the "Prototype states" screen). A
 * firmware update is offered only in the FirmwareUpdate scenario; installing it
 * resets the scenario to Normal.
 */
class DemoDeviceRepository(
    private val scope: CoroutineScope,
    private val demoState: DemoStateController,
) : DeviceRepository {

    private val _removed = MutableStateFlow(false)

    override val device: StateFlow<DeviceSnapshot?> =
        combine(demoState.deviceScenario, _removed) { scenario, removed ->
            if (removed) null else snapshot(scenario)
        }.stateIn(scope, SharingStarted.Eagerly, snapshot(demoState.deviceScenario.value))

    override val diagnostics: StateFlow<List<DiagnosticEntry>> =
        MutableStateFlow(
            listOf(
                DiagnosticEntry("USB storage bridge", "Active"),
                DiagnosticEntry("Event detection latency", "1.2 s"),
                DiagnosticEntry("Uploads (24h)", "18 · 0 failed"),
                DiagnosticEntry("Uptime", "6d 4h"),
            ),
        )

    override val firmwareUpdate: StateFlow<FirmwareUpdate?> =
        demoState.deviceScenario
            .map { if (it == DeviceScenario.FirmwareUpdate) FIRMWARE_UPDATE else null }
            .stateIn(
                scope,
                SharingStarted.Eagerly,
                if (demoState.deviceScenario.value == DeviceScenario.FirmwareUpdate) FIRMWARE_UPDATE else null,
            )

    override suspend fun runConnectionTest(): List<ConnectionTestStep> {
        delay(1200)
        return CONNECTION_TEST_STEPS
    }

    override suspend fun installFirmwareUpdate() {
        delay(1500)
        demoState.deviceScenario.value = DeviceScenario.Normal
    }

    override suspend fun restart() {
        delay(1500)
    }

    override suspend fun factoryReset() {
        delay(1500)
        _removed.value = true
    }

    override suspend fun remove() {
        delay(600)
        _removed.value = true
    }

    private fun snapshot(scenario: DeviceScenario): DeviceSnapshot = when (scenario) {
        DeviceScenario.Normal -> dev(
            condition = DeviceCondition.Online,
            statusText = "Online · Wi-Fi",
            sentry = "Watching",
            storage = 73,
            power = "Healthy",
            temp = "Normal",
            fw = "v4.0.2 · current",
            fwUpd = false,
            backlog = 0,
            ble = true,
            wifi = true,
            backend = true,
            lastSeen = "Just now",
            writing = false,
            issues = emptyList(),
        )
        DeviceScenario.Offline -> dev(
            condition = DeviceCondition.Offline,
            statusText = "Offline",
            sentry = "Unknown",
            storage = 73,
            power = "Healthy",
            temp = "—",
            fw = "v4.0.2",
            fwUpd = false,
            backlog = 3,
            ble = false,
            wifi = false,
            backend = false,
            lastSeen = "2:31 PM",
            writing = false,
            issues = listOf("No connection to Garage Pi for 21 min"),
        )
        DeviceScenario.LowStorage -> dev(
            condition = DeviceCondition.NeedsAttention,
            statusText = "Low storage",
            sentry = "Watching",
            storage = 6,
            power = "Healthy",
            temp = "Normal",
            fw = "v4.0.2 · current",
            fwUpd = false,
            backlog = 0,
            ble = true,
            wifi = true,
            backend = true,
            lastSeen = "Just now",
            writing = false,
            issues = listOf("Only 6% storage free — oldest clips will be reclaimed"),
        )
        DeviceScenario.Overheating -> dev(
            condition = DeviceCondition.NeedsAttention,
            statusText = "Running hot",
            sentry = "Watching",
            storage = 61,
            power = "Healthy",
            temp = "High · 71°C",
            fw = "v4.0.2 · current",
            fwUpd = false,
            backlog = 0,
            ble = true,
            wifi = true,
            backend = true,
            lastSeen = "Just now",
            writing = false,
            issues = listOf("Device temperature is high — check ventilation"),
        )
        DeviceScenario.WeakPower -> dev(
            condition = DeviceCondition.NeedsAttention,
            statusText = "Weak power",
            sentry = "Watching",
            storage = 68,
            power = "Weak · 4.6V",
            temp = "Normal",
            fw = "v4.0.2 · current",
            fwUpd = false,
            backlog = 0,
            ble = true,
            wifi = true,
            backend = true,
            lastSeen = "Just now",
            writing = false,
            issues = listOf("USB power is below spec — use a higher-output port"),
        )
        DeviceScenario.Backlog -> dev(
            condition = DeviceCondition.Online,
            statusText = "Uploading",
            sentry = "Watching",
            storage = 44,
            power = "Healthy",
            temp = "Normal",
            fw = "v4.0.2 · current",
            fwUpd = false,
            backlog = 12,
            ble = true,
            wifi = true,
            backend = true,
            lastSeen = "Just now",
            writing = true,
            issues = listOf("12 clips queued to upload"),
        )
        DeviceScenario.FirmwareUpdate -> dev(
            condition = DeviceCondition.UpdateRequired,
            statusText = "Update ready",
            sentry = "Watching",
            storage = 73,
            power = "Healthy",
            temp = "Normal",
            fw = "v3.8.1 → v4.0.2",
            fwUpd = true,
            backlog = 0,
            ble = true,
            wifi = true,
            backend = true,
            lastSeen = "Just now",
            writing = false,
            issues = listOf("Firmware update available"),
        )
        DeviceScenario.BackendDown -> dev(
            condition = DeviceCondition.LocalOnly,
            statusText = "Local only",
            sentry = "Watching",
            storage = 73,
            power = "Healthy",
            temp = "Normal",
            fw = "v4.0.2 · current",
            fwUpd = false,
            backlog = 0,
            ble = true,
            wifi = true,
            backend = false,
            lastSeen = "Just now",
            writing = false,
            issues = listOf("Backend unreachable — local Wi-Fi still works"),
        )
    }

    private fun dev(
        condition: DeviceCondition,
        statusText: String,
        sentry: String,
        storage: Int,
        power: String,
        temp: String,
        fw: String,
        fwUpd: Boolean,
        backlog: Int,
        ble: Boolean,
        wifi: Boolean,
        backend: Boolean,
        lastSeen: String,
        writing: Boolean,
        issues: List<String>,
    ) = DeviceSnapshot(
        id = "garage-pi",
        name = "Garage Pi",
        vehicleModel = "Model 3",
        vehicleNickname = "Daily driver",
        location = "Downtown Garage",
        condition = condition,
        statusText = statusText,
        sentryStatus = sentry,
        storageFreePct = storage,
        powerStatus = power,
        temperatureStatus = temp,
        firmwareVersion = fw,
        firmwareUpdateAvailable = fwUpd,
        uploadBacklog = backlog,
        bleConnected = ble,
        wifiConnected = wifi,
        wifiNetwork = if (wifi) "Garage 5G" else null,
        backendConnected = backend,
        lastSeen = lastSeen,
        recordingNow = writing,
        issues = issues,
        signalStrength = "Strong",
    )

    companion object {
        val FIRMWARE_UPDATE = FirmwareUpdate(
            currentVersion = "v3.8.1",
            newVersion = "v4.0.2",
            summary = "Improves event detection accuracy and reduces false positives. The device restarts once and is offline for about a minute.",
            detailLine = "~2 min · device restarts once",
        )

        val CONNECTION_TEST_STEPS = listOf(
            ConnectionTestStep("Bluetooth", "Paired with Garage Pi", true),
            ConnectionTestStep("Wi-Fi", "Connected to Garage 5G", true),
            ConnectionTestStep("Backend", "Linked to your Sentyx account", true),
            ConnectionTestStep("Event detection", "USB storage bridge active", true),
        )
    }
}

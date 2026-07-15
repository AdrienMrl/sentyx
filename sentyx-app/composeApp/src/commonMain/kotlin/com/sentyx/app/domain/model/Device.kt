package com.sentyx.app.domain.model

/** High-level device condition driving the status chip and issue banners. */
enum class DeviceCondition { Online, Offline, NeedsAttention, UpdateRequired, LocalOnly }

/** Snapshot of the in-car Pi's state, as reported over BLE/Wi-Fi/backend. */
data class DeviceSnapshot(
    val id: String,
    val name: String,
    val vehicleModel: String,
    val vehicleNickname: String? = null,
    val location: String,
    val condition: DeviceCondition,
    /** Short status chip text, e.g. "Online · Wi-Fi", "Low storage". */
    val statusText: String,
    /** Sentry Mode status as seen by the Pi, e.g. "Watching", "Unknown". */
    val sentryStatus: String,
    val storageFreePct: Int,
    val powerStatus: String,
    val temperatureStatus: String,
    val firmwareVersion: String,
    val firmwareUpdateAvailable: Boolean,
    /** Clips spooled on the Pi awaiting upload. */
    val uploadBacklog: Int,
    val bleConnected: Boolean,
    val wifiConnected: Boolean,
    /** Wi-Fi network name when connected. */
    val wifiNetwork: String? = null,
    val backendConnected: Boolean,
    val lastSeen: String,
    /** True while the car is actively writing an event clip. */
    val recordingNow: Boolean,
    /** Human-readable issues to surface in warning banners. */
    val issues: List<String> = emptyList(),
    val signalStrength: String = "Strong",
)

/** One row of the diagnostics table. */
data class DiagnosticEntry(val label: String, val value: String, val healthy: Boolean = true)

/** One step of the connection test (BLE, Wi-Fi, backend, detection bridge). */
data class ConnectionTestStep(val title: String, val subtitle: String, val passed: Boolean)

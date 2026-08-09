package com.sentyx.app.data.device

import com.sentyx.app.core.platform.currentEpochMillis
import com.sentyx.app.core.storage.KeyValueStore
import com.sentyx.app.core.storage.StorageKeys
import com.sentyx.app.data.api.DeviceNotFoundException
import com.sentyx.app.data.api.DeviceStatusDto
import com.sentyx.app.data.api.DeviceUpdateDto
import com.sentyx.app.data.api.HeartbeatDto
import com.sentyx.app.data.api.SentyxApi
import com.sentyx.app.domain.model.ConnectionTestStep
import com.sentyx.app.domain.model.DeviceCondition
import com.sentyx.app.domain.model.DeviceSnapshot
import com.sentyx.app.domain.model.DiagnosticEntry
import com.sentyx.app.domain.model.FirmwareStage
import com.sentyx.app.domain.model.FirmwareUpdate
import com.sentyx.app.domain.repository.DeviceRepository
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlin.math.round
import kotlin.math.roundToInt

/**
 * Real [DeviceRepository] backed by the Sentyx backend. Polls
 * `GET /v1/devices/{deviceId}` every [POLL_INTERVAL_MS] and maps the server's
 * device-status document onto a [DeviceSnapshot] (see the M6 heartbeat contract).
 *
 * The paired device id is read from [KeyValueStore] on every tick, so the flow
 * self-heals across the lifecycle: null before onboarding persists an id, live
 * afterwards, and back to null once [remove] clears it. Missing/nullable metrics
 * map to honest fallbacks ("Unknown"), never to fabricated numbers.
 */
class RealDeviceRepository(
    private val scope: CoroutineScope,
    private val api: SentyxApi,
    private val store: KeyValueStore,
) : DeviceRepository {

    private val _device = MutableStateFlow<DeviceSnapshot?>(null)
    override val device: StateFlow<DeviceSnapshot?> = _device.asStateFlow()

    private val _diagnostics = MutableStateFlow<List<DiagnosticEntry>>(emptyList())
    override val diagnostics: StateFlow<List<DiagnosticEntry>> = _diagnostics.asStateFlow()

    private val _firmwareUpdate = MutableStateFlow<FirmwareUpdate?>(null)
    override val firmwareUpdate: StateFlow<FirmwareUpdate?> = _firmwareUpdate.asStateFlow()

    /**
     * Last agent version seen in a heartbeat, so scheduling an update can label
     * the "from" side without waiting for the next poll. Null until the device
     * has reported at least once.
     */
    private var installedVersion: String? = null

    init {
        scope.launch { pollLoop() }
    }

    private suspend fun pollLoop() {
        while (true) {
            val deviceId = store.getString(StorageKeys.DEVICE_ID)
            if (deviceId == null) {
                _device.value = null
                _diagnostics.value = emptyList()
                _firmwareUpdate.value = null
            } else {
                fetch(deviceId)
            }
            delay(POLL_INTERVAL_MS)
        }
    }

    private suspend fun fetch(deviceId: String) {
        try {
            val dto = api.deviceStatus(deviceId)
            val name = store.getString(StorageKeys.DEVICE_NAME) ?: dto.name
            _device.value = mapDeviceSnapshot(dto, name, currentEpochMillis())
            _diagnostics.value = buildDiagnostics(dto.status)
            installedVersion = dto.status?.agentVersion
            _firmwareUpdate.value = mapFirmwareUpdate(dto)
        } catch (e: DeviceNotFoundException) {
            // The backend doesn't know this device (yet) — surface "no device".
            _device.value = null
            _diagnostics.value = emptyList()
            _firmwareUpdate.value = null
        } catch (e: CancellationException) {
            throw e
        } catch (e: Throwable) {
            // Poll failure (server unreachable): keep the last snapshot but mark
            // it offline so the UI stops implying a live link.
            val last = _device.value
            if (last != null) {
                _device.value = last.copy(
                    condition = DeviceCondition.Offline,
                    statusText = "Offline",
                    backendConnected = false,
                    wifiConnected = false,
                )
            }
        }
    }

    override suspend fun runConnectionTest(): List<ConnectionTestStep> {
        val steps = mutableListOf<ConnectionTestStep>()
        val healthOk = try {
            api.healthz()
        } catch (e: CancellationException) {
            throw e
        } catch (e: Throwable) {
            false
        }
        steps += ConnectionTestStep(
            title = "Server reachable",
            subtitle = if (healthOk) "Backend responded to /healthz" else "Couldn't reach the server",
            passed = healthOk,
        )
        val authOk = try {
            api.sessionValid()
        } catch (e: CancellationException) {
            throw e
        } catch (e: Throwable) {
            false
        }
        steps += ConnectionTestStep(
            title = "Backend authenticated",
            subtitle = if (authOk) "Signed-in session accepted" else "Authentication failed",
            passed = authOk,
        )
        return steps
    }

    /**
     * Asks the backend to deliver the newest release to this device. It returns
     * as soon as the update is scheduled, not when it is installed: the device
     * pulls, verifies and installs on its own, and the poll loop reports how far
     * it has got. The server's reply is applied immediately so the UI moves off
     * "available" without waiting for the next tick.
     */
    override suspend fun installFirmwareUpdate() {
        val deviceId = store.getString(StorageKeys.DEVICE_ID)
            ?: throw IllegalStateException("No device is paired.")
        val update = api.requestDeviceUpdate(deviceId)
        _firmwareUpdate.value = mapFirmwareUpdate(update, installedVersion)
    }

    override suspend fun restart() {
        // TODO: no remote-restart endpoint yet (no control channel to the Pi off
        // the home screen). No-op stub — the UI's optimistic toast still shows.
    }

    override suspend fun factoryReset() {
        // TODO: cannot command a remote factory reset yet. No-op stub; use
        // remove() to forget the device locally.
    }

    override suspend fun remove() {
        store.remove(StorageKeys.DEVICE_ID)
        store.remove(StorageKeys.DEVICE_NAME)
        _device.value = null
        _diagnostics.value = emptyList()
        _firmwareUpdate.value = null
        installedVersion = null
    }

    companion object {
        private const val POLL_INTERVAL_MS = 15_000L
    }
}

// ---- Mapping (pure, unit-tested) --------------------------------------------

/** Storage below this percent (when known) counts as "low" / NeedsAttention. */
private const val LOW_STORAGE_PCT = 10

/** Upload backlog at or above this counts as "high" / NeedsAttention. */
private const val HIGH_BACKLOG = 10

/** Sentinel for [DeviceSnapshot.storageFreePct] when storage bytes are unknown. */
internal const val UNKNOWN_STORAGE_PCT = -1

/**
 * Map a server device-status document onto a [DeviceSnapshot] per the M6
 * contract. [deviceName] is the locally-remembered name (fallback if the server
 * name is blank); [nowMs] is the current wall-clock time for humanizing "last seen".
 */
internal fun mapDeviceSnapshot(dto: DeviceStatusDto, deviceName: String, nowMs: Long): DeviceSnapshot {
    val st = dto.status
    val online = dto.online
    val lastSeenMs = dto.lastSeenMs

    val storagePct = storageFreePct(st)
    val storageKnown = storagePct != UNKNOWN_STORAGE_PCT
    val lowStorage = storageKnown && storagePct <= LOW_STORAGE_PCT
    val underVoltageNow = st?.underVoltageNow == true
    val backlog = st?.uploadBacklog ?: 0
    val highBacklog = backlog >= HIGH_BACKLOG
    val wifiSsid = st?.wifiSsid

    val condition = when {
        !online -> DeviceCondition.Offline
        underVoltageNow || lowStorage || highBacklog -> DeviceCondition.NeedsAttention
        else -> DeviceCondition.Online
    }

    val statusText = when {
        !online -> "Offline"
        wifiSsid != null -> "Online · Wi-Fi"
        else -> "Online"
    }

    val sentryStatus = when (st?.sentryActive) {
        true -> "Watching"
        false -> "Idle"
        null -> "Unknown"
    }

    val powerStatus = when {
        st?.underVoltageNow == true -> "Under-voltage"
        st?.underVoltageEver == true -> "Voltage dips seen"
        st?.underVoltageNow == false || st?.underVoltageEver == false -> "Healthy"
        else -> "Unknown"
    }

    val temperatureStatus = st?.cpuTempC?.let { t ->
        when {
            t < 70.0 -> "Normal"
            t < 80.0 -> "Warm"
            else -> "Hot"
        }
    } ?: "Unknown"

    val update = mapFirmwareUpdate(dto)

    val firmwareVersion = when {
        st?.agentVersion == null -> "Unknown"
        update != null -> "${st.agentVersion} → ${update.newVersion}"
        else -> "${st.agentVersion} · current"
    }

    val signalStrength = st?.wifiSignalPct?.let { p ->
        when {
            p >= 70 -> "Strong"
            p >= 40 -> "Fair"
            else -> "Weak"
        }
    } ?: "Unknown"

    val lastSeenText = if (lastSeenMs != null) humanizeSince(nowMs - lastSeenMs) else "Never"

    val issues = buildList {
        if (underVoltageNow) add("Under-voltage detected")
        if (lowStorage) add("Low storage ($storagePct%)")
        if (highBacklog) add("$backlog clips waiting to upload")
        if (!online) add("Device offline — last seen $lastSeenText")
    }

    return DeviceSnapshot(
        id = dto.deviceId,
        name = dto.name.ifBlank { deviceName },
        vehicleModel = "Unknown",
        vehicleNickname = null,
        location = "Unknown",
        condition = condition,
        statusText = statusText,
        sentryStatus = sentryStatus,
        storageFreePct = storagePct,
        powerStatus = powerStatus,
        temperatureStatus = temperatureStatus,
        firmwareVersion = firmwareVersion,
        firmwareUpdateAvailable = update != null,
        uploadBacklog = backlog,
        bleConnected = false,
        wifiConnected = online,
        wifiNetwork = wifiSsid,
        backendConnected = online,
        lastSeen = lastSeenText,
        recordingNow = st?.recordingNow == true,
        issues = issues,
        signalStrength = signalStrength,
    )
}

/**
 * Map the server's firmware block onto a [FirmwareUpdate], or null when there is
 * nothing for the user to act on or watch: no release has ever been offered to
 * this device, or the last one finished and nothing newer is pending. A finished
 * install is not an "update" — it shows up as the device's current version.
 *
 * A failed or reverted install stays non-null on purpose: the user should see
 * that it didn't take, and be able to retry.
 */
internal fun mapFirmwareUpdate(dto: DeviceStatusDto): FirmwareUpdate? =
    mapFirmwareUpdate(dto.update, dto.status?.agentVersion)

internal fun mapFirmwareUpdate(update: DeviceUpdateDto?, installedVersion: String?): FirmwareUpdate? {
    if (update == null) return null
    val stage = firmwareStage(update) ?: return null
    val current = installedVersion ?: "Unknown"
    // Blank rather than "Unknown": the version is only unknown before the device
    // has ever reported, at which point there is no offer to label either.
    val next = update.version.ifBlank { "the newest version" }
    val error = update.error.ifBlank { null }

    val summary = when (stage) {
        FirmwareStage.Available ->
            update.notes.ifBlank { "No release notes were published for this version." }
        FirmwareStage.Requested ->
            "Scheduled. Your device will pick it up on its next check-in."
        FirmwareStage.Downloading ->
            "Downloading to your device. It keeps recording throughout."
        FirmwareStage.WaitingSafe ->
            "Downloaded and verified. It installs the next time your car isn't recording, " +
                "so this can sit here for a while — nothing is wrong."
        FirmwareStage.Installing ->
            "Installing on your device."
        FirmwareStage.Rebooting ->
            "Restarting your device. It's offline for about a minute."
        FirmwareStage.Failed ->
            "The update didn't complete. Your device is still on $current and still recording."
        FirmwareStage.RolledBack ->
            "The update was installed, failed its health check, and was reverted automatically. " +
                "Your device is back on $current and still recording."
    }

    val detailLine = when {
        error != null -> error
        update.progressPct in 1..99 -> "${update.progressPct}% · ${stage.label}"
        stage == FirmwareStage.Available -> "Installs when recording is idle · device restarts once"
        else -> stage.label
    }

    return FirmwareUpdate(
        currentVersion = current,
        newVersion = next,
        summary = summary,
        detailLine = detailLine,
        stage = stage,
        progressPct = update.progressPct,
        errorMessage = error,
    )
}

/**
 * The device's own reported state wins over the offer flag: a unit can be
 * mid-reboot with the offer already withdrawn. Null means "nothing to show".
 */
private fun firmwareStage(update: DeviceUpdateDto): FirmwareStage? = when (update.state) {
    "offered" -> FirmwareStage.Requested
    "downloading" -> FirmwareStage.Downloading
    "waiting-safe" -> FirmwareStage.WaitingSafe
    "installing" -> FirmwareStage.Installing
    "rebooting" -> FirmwareStage.Rebooting
    "failed" -> FirmwareStage.Failed
    "rolled-back" -> FirmwareStage.RolledBack
    "installed" -> if (update.available) FirmwareStage.Available else null
    else -> if (update.available) FirmwareStage.Available else null
}

/** Real diagnostics rows from the heartbeat; unknown metrics are omitted rather than faked. */
internal fun buildDiagnostics(st: HeartbeatDto?): List<DiagnosticEntry> {
    if (st == null) return emptyList()
    return buildList {
        st.uptimeSec?.let { add(DiagnosticEntry("Uptime", humanizeUptime(it))) }
        st.cpuTempC?.let {
            add(DiagnosticEntry("CPU temperature", "${oneDecimal(it)} °C", healthy = it < 80.0))
        }
        st.wifiRssiDbm?.let {
            add(DiagnosticEntry("Wi-Fi signal", "$it dBm", healthy = it > -70))
        }
        st.uploadBacklog?.let {
            add(
                DiagnosticEntry(
                    label = "Upload backlog",
                    value = if (it == 0) "None" else "$it clips",
                    healthy = it < HIGH_BACKLOG,
                ),
            )
        }
        val pct = storageFreePct(st)
        if (pct != UNKNOWN_STORAGE_PCT) {
            add(DiagnosticEntry("Storage free", "$pct%", healthy = pct > LOW_STORAGE_PCT))
        }
    }
}

private fun storageFreePct(st: HeartbeatDto?): Int {
    val free = st?.storageFreeBytes ?: return UNKNOWN_STORAGE_PCT
    val total = st.storageTotalBytes ?: return UNKNOWN_STORAGE_PCT
    if (total <= 0L) return UNKNOWN_STORAGE_PCT
    return ((free.toDouble() / total.toDouble()) * 100.0).roundToInt()
}

private fun oneDecimal(value: Double): String {
    val rounded = round(value * 10.0) / 10.0
    return rounded.toString()
}

/** Humanize a "time since" delta in millis: "Just now", "Nm ago", "Nh ago", "Nd ago". */
internal fun humanizeSince(deltaMs: Long): String {
    if (deltaMs < 0L) return "Just now"
    val seconds = deltaMs / 1000L
    return when {
        seconds < 60L -> "Just now"
        seconds < 3600L -> "${seconds / 60L}m ago"
        seconds < 86_400L -> "${seconds / 3600L}h ago"
        else -> "${seconds / 86_400L}d ago"
    }
}

/** Humanize an uptime in seconds: "Nd Nh", "Nh Nm", or "Nm". */
internal fun humanizeUptime(seconds: Long): String {
    val days = seconds / 86_400L
    val hours = (seconds % 86_400L) / 3600L
    val minutes = (seconds % 3600L) / 60L
    return when {
        days > 0L -> "${days}d ${hours}h"
        hours > 0L -> "${hours}h ${minutes}m"
        else -> "${minutes}m"
    }
}

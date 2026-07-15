package com.sentyx.app.domain.repository

import com.sentyx.app.domain.model.ConnectionTestStep
import com.sentyx.app.domain.model.DeviceSnapshot
import com.sentyx.app.domain.model.DiagnosticEntry
import com.sentyx.app.domain.model.FirmwareUpdate
import kotlinx.coroutines.flow.StateFlow

/** State + maintenance of the paired in-car Pi. */
interface DeviceRepository {
    /** Null when no device is paired. */
    val device: StateFlow<DeviceSnapshot?>

    val diagnostics: StateFlow<List<DiagnosticEntry>>

    /** Pending firmware update, null when current. */
    val firmwareUpdate: StateFlow<FirmwareUpdate?>

    suspend fun runConnectionTest(): List<ConnectionTestStep>
    suspend fun installFirmwareUpdate()
    suspend fun restart()
    suspend fun factoryReset()
    suspend fun remove()
}

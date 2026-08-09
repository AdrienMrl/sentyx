package com.sentyx.app.feature.device

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.core.ui.ToastData
import com.sentyx.app.core.ui.ToastTone
import com.sentyx.app.domain.model.ConnectionTestStep
import com.sentyx.app.domain.model.DeviceSnapshot
import com.sentyx.app.domain.model.DiagnosticEntry
import com.sentyx.app.domain.model.FirmwareUpdate
import com.sentyx.app.domain.repository.DeviceRepository
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/**
 * UI state for the device tab and its sub-screens. Combines the reactive
 * repository state (snapshot / diagnostics / firmware offer) with transient,
 * in-flight state owned by the ViewModel (connection test, firmware install).
 */
data class DeviceUiState(
    /** Null once the device is removed or factory-reset (or not yet paired). */
    val snapshot: DeviceSnapshot?,
    val diagnostics: List<DiagnosticEntry>,
    val firmwareUpdate: FirmwareUpdate?,
    val connectionTestInProgress: Boolean = false,
    val connectionTestResults: List<ConnectionTestStep> = emptyList(),
    val firmwareInstalling: Boolean = false,
)

private data class TransientState(
    val connectionTestInProgress: Boolean = false,
    val connectionTestResults: List<ConnectionTestStep> = emptyList(),
    val firmwareInstalling: Boolean = false,
)

/**
 * Drives the Device overview, settings, firmware, diagnostics, factory-reset,
 * and add-device screens. Reads the paired Pi's state from [DeviceRepository]
 * and runs maintenance actions in [viewModelScope]. It never navigates itself —
 * actions that should advance the flow take an `onComplete` callback the screen
 * wires to its navigation lambda.
 */
class DeviceViewModel(
    private val device: DeviceRepository,
    private val toasts: ToastController,
) : ViewModel() {

    private val _transient = MutableStateFlow(TransientState())

    val state: StateFlow<DeviceUiState> =
        combine(
            device.device,
            device.diagnostics,
            device.firmwareUpdate,
            _transient,
        ) { snapshot, diagnostics, firmware, transient ->
            DeviceUiState(
                snapshot = snapshot,
                diagnostics = diagnostics,
                firmwareUpdate = firmware,
                connectionTestInProgress = transient.connectionTestInProgress,
                connectionTestResults = transient.connectionTestResults,
                firmwareInstalling = transient.firmwareInstalling,
            )
        }.stateIn(
            viewModelScope,
            SharingStarted.Eagerly,
            DeviceUiState(
                snapshot = device.device.value,
                diagnostics = device.diagnostics.value,
                firmwareUpdate = device.firmwareUpdate.value,
            ),
        )

    /** Runs the connection test and records its steps for display. */
    fun runConnectionTest() {
        if (_transient.value.connectionTestInProgress) return
        _transient.update { it.copy(connectionTestInProgress = true, connectionTestResults = emptyList()) }
        viewModelScope.launch {
            try {
                val results = device.runConnectionTest()
                _transient.update { it.copy(connectionTestResults = results) }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                showError(e)
            } finally {
                _transient.update { it.copy(connectionTestInProgress = false) }
            }
        }
    }

    /**
     * Schedules the pending firmware update. This only asks the backend to
     * deliver it — the device downloads, verifies and installs on its own, and
     * only once it isn't recording. [firmwareInstalling] therefore covers just
     * the request round-trip; the install itself is reported through
     * [DeviceUiState.firmwareUpdate] as the device makes progress, so the screen
     * stays put rather than navigating away on a completion that hasn't happened.
     */
    fun installFirmware() {
        if (_transient.value.firmwareInstalling) return
        val target = state.value.firmwareUpdate?.newVersion
        _transient.update { it.copy(firmwareInstalling = true) }
        viewModelScope.launch {
            try {
                device.installFirmwareUpdate()
                toasts.show(
                    ToastData(
                        title = "Update scheduled",
                        subtitle = target?.let { "$it installs when your car isn't recording" }
                            ?: "It installs when your car isn't recording",
                        tone = ToastTone.Success,
                    ),
                )
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                showError(e)
            } finally {
                _transient.update { it.copy(firmwareInstalling = false) }
            }
        }
    }

    /** Restarts the device (mock); surfaces an info toast immediately. */
    fun restart() {
        val name = state.value.snapshot?.name ?: "Device"
        toasts.show(
            ToastData(
                title = "Restarting device",
                subtitle = "$name will be back in about a minute",
                tone = ToastTone.Info,
            ),
        )
        viewModelScope.launch {
            try {
                device.restart()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                showError(e)
            }
        }
    }

    /** Factory-resets the device (mock); the snapshot goes null when done. */
    fun factoryReset() {
        val name = state.value.snapshot?.name ?: "Device"
        toasts.show(
            ToastData(
                title = "Device reset",
                subtitle = "$name has been erased",
                tone = ToastTone.Success,
            ),
        )
        viewModelScope.launch {
            try {
                device.factoryReset()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                showError(e)
            }
        }
    }

    /** Removes the device from the account (mock). */
    fun remove() {
        val name = state.value.snapshot?.name ?: "Device"
        toasts.show(
            ToastData(
                title = "Device removed",
                subtitle = "$name is no longer linked to your account",
                tone = ToastTone.Info,
            ),
        )
        viewModelScope.launch {
            try {
                device.remove()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                showError(e)
            }
        }
    }

    /** Mock affordance for settings rows that aren't wired in this preview. */
    fun comingSoon(label: String) {
        toasts.show(
            ToastData(
                title = label,
                subtitle = "Not available in this preview",
                tone = ToastTone.Info,
            ),
        )
    }

    private fun showError(e: Throwable) {
        toasts.show(
            ToastData(
                title = "Something went wrong",
                subtitle = e.message ?: "Please try again",
                tone = ToastTone.Urgent,
            ),
        )
    }
}

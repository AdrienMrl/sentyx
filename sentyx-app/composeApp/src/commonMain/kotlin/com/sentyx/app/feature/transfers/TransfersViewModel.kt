package com.sentyx.app.feature.transfers

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.core.ui.ToastData
import com.sentyx.app.core.ui.ToastTone
import com.sentyx.app.domain.model.Transfer
import com.sentyx.app.domain.model.TransferRoute
import com.sentyx.app.domain.model.TransferSettings
import com.sentyx.app.domain.model.TransferStatus
import com.sentyx.app.domain.repository.TransferRepository
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch

/** UI state for the transfers tab: the active queue, the completed library, and settings. */
data class TransfersUiState(
    /** Transfers still in flight (status != Complete), newest first. */
    val active: List<Transfer>,
    /** Completed transfers that form the on-phone library. */
    val library: List<Transfer>,
    val settings: TransferSettings,
    /** Route availability from the repository (e.g. Bluetooth off ⇒ unavailable). */
    val routeAvailability: List<Pair<TransferRoute, Boolean>>,
)

/**
 * Drives the Transfers tab, the download library, and transfer settings. Splits
 * the repository's transfer list into the active queue and the completed library,
 * and exposes the per-status queue actions plus settings mutations.
 */
class TransfersViewModel(
    private val transfers: TransferRepository,
    private val toasts: ToastController,
) : ViewModel() {

    val uiState: StateFlow<TransfersUiState> =
        combine(transfers.transfers, transfers.settings) { list, settings ->
            TransfersUiState(
                active = list.filter { it.status != TransferStatus.Complete },
                library = list.filter { it.status == TransferStatus.Complete },
                settings = settings,
                routeAvailability = transfers.availableRoutes(),
            )
        }.stateIn(
            scope = viewModelScope,
            started = SharingStarted.WhileSubscribed(5_000),
            initialValue = TransfersUiState(
                active = emptyList(),
                library = emptyList(),
                settings = transfers.settings.value,
                routeAvailability = transfers.availableRoutes(),
            ),
        )

    fun pause(id: String) = viewModelScope.launch { transfers.pause(id) }

    fun resume(id: String) = viewModelScope.launch { transfers.resume(id) }

    fun retry(id: String) = viewModelScope.launch { transfers.retry(id) }

    fun cancel(id: String) = viewModelScope.launch { transfers.cancel(id) }

    fun remove(id: String) = viewModelScope.launch { transfers.remove(id) }

    /** Mock playback of a completed clip: shows an in-app toast (no real player). */
    fun play() {
        toasts.show(
            ToastData(
                title = "Playing clip",
                subtitle = "Mock player",
                tone = ToastTone.Info,
            ),
        )
    }

    fun selectRoute(route: TransferRoute) = viewModelScope.launch {
        transfers.updateSettings(uiState.value.settings.copy(preferredRoute = route))
    }

    fun setAutoUploadNewClips(enabled: Boolean) = viewModelScope.launch {
        transfers.updateSettings(uiState.value.settings.copy(autoUploadNewClips = enabled))
    }

    fun setOnlyMostRelevantCamera(enabled: Boolean) = viewModelScope.launch {
        transfers.updateSettings(uiState.value.settings.copy(onlyMostRelevantCamera = enabled))
    }

    fun setAllowCellularDownloads(enabled: Boolean) = viewModelScope.launch {
        transfers.updateSettings(uiState.value.settings.copy(allowCellularDownloads = enabled))
    }
}

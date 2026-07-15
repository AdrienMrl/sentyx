package com.sentyx.app.feature.settings

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.core.ui.ToastData
import com.sentyx.app.core.ui.ToastTone
import com.sentyx.app.domain.model.SavedWifiNetwork
import com.sentyx.app.domain.model.WifiNetwork
import com.sentyx.app.domain.model.WifiStatus
import com.sentyx.app.domain.repository.DeviceWifiService
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/** BLE link phase for the settings session (drives the top-level screen state). */
enum class WifiLinkPhase { Connecting, Ready, Failed }

/**
 * The add-a-network sub-flow overlaying the list: scan → pick → (password) →
 * connecting. Non-null on [SavedWifiUiState.add] while the flow is open.
 */
data class AddNetworkState(
    val scanning: Boolean = true,
    val networks: List<WifiNetwork> = emptyList(),
    val scanError: String? = null,
    /** Non-null while the password sheet for a secured network is open. */
    val passwordFor: WifiNetwork? = null,
    val password: String = "",
    /** Non-null while a join is in flight (Wi-Fi briefly drops). */
    val connectingSsid: String? = null,
    val connectError: String? = null,
)

/** UI state for [SavedWifiNetworksScreen]. */
data class SavedWifiUiState(
    val phase: WifiLinkPhase = WifiLinkPhase.Connecting,
    /** Message for [WifiLinkPhase.Failed]. */
    val linkError: String? = null,
    val status: WifiStatus? = null,
    /** Inline banner when a status refresh failed but the link is still up. */
    val statusError: String? = null,
    /** Non-null while a forget-confirmation dialog is shown. */
    val forgetTarget: SavedWifiNetwork? = null,
    /** Non-null while a forget is in flight. */
    val forgettingSsid: String? = null,
    val add: AddNetworkState? = null,
)

/**
 * Drives [SavedWifiNetworksScreen]. Acquires the BLE link on init (connect →
 * authenticate → first status), exposes the current + saved networks, and runs
 * the forget / add-network flows. It never navigates; the screen owns that. The
 * link is released in [onCleared] so leaving the screen frees the connection.
 */
class SavedWifiNetworksViewModel(
    private val wifi: DeviceWifiService,
    private val toasts: ToastController,
) : ViewModel() {

    private val _state = MutableStateFlow(SavedWifiUiState())
    val state: StateFlow<SavedWifiUiState> = _state.asStateFlow()

    init {
        connect()
    }

    // ---- Link ---------------------------------------------------------------

    fun connect() {
        _state.update { it.copy(phase = WifiLinkPhase.Connecting, linkError = null) }
        viewModelScope.launch {
            try {
                wifi.connect()
                val status = wifi.status()
                _state.update { it.copy(phase = WifiLinkPhase.Ready, status = status, statusError = null) }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                _state.update {
                    it.copy(
                        phase = WifiLinkPhase.Failed,
                        linkError = e.message ?: "Couldn't connect to your Sentyx Pi.",
                    )
                }
            }
        }
    }

    private fun refreshStatus() {
        viewModelScope.launch {
            try {
                val status = wifi.status()
                _state.update { it.copy(status = status, statusError = null) }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                _state.update { it.copy(statusError = e.message ?: "Couldn't refresh the Wi-Fi status.") }
            }
        }
    }

    fun retryStatus() {
        _state.update { it.copy(statusError = null) }
        refreshStatus()
    }

    // ---- Forget -------------------------------------------------------------

    fun requestForget(network: SavedWifiNetwork) =
        _state.update { it.copy(forgetTarget = network) }

    fun cancelForget() = _state.update { it.copy(forgetTarget = null) }

    fun confirmForget() {
        val target = _state.value.forgetTarget ?: return
        _state.update { it.copy(forgetTarget = null, forgettingSsid = target.ssid) }
        viewModelScope.launch {
            try {
                wifi.forget(target.ssid)
                val status = wifi.status()
                _state.update { it.copy(status = status, forgettingSsid = null) }
                toasts.show(
                    ToastData(
                        title = "Network forgotten",
                        subtitle = "Removed \"${target.ssid}\"",
                        tone = ToastTone.Info,
                    ),
                )
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                _state.update { it.copy(forgettingSsid = null) }
                showError(e)
            }
        }
    }

    // ---- Add a network ------------------------------------------------------

    fun openAdd() {
        _state.update { it.copy(add = AddNetworkState(scanning = true)) }
        scan()
    }

    fun closeAdd() = _state.update { it.copy(add = null) }

    fun rescan() {
        _state.update { it.copy(add = it.add?.copy(scanning = true, scanError = null)) }
        scan()
    }

    private fun scan() {
        viewModelScope.launch {
            try {
                val networks = wifi.scan()
                _state.update { s ->
                    // Ignore a stale result if the user closed the sheet meanwhile.
                    if (s.add == null) s else s.copy(add = s.add.copy(scanning = false, networks = networks))
                }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                _state.update { s ->
                    if (s.add == null) s else s.copy(
                        add = s.add.copy(scanning = false, scanError = e.message ?: "Couldn't scan for networks."),
                    )
                }
            }
        }
    }

    fun pickNetwork(network: WifiNetwork) {
        if (network.requiresPassword) {
            _state.update { it.copy(add = it.add?.copy(passwordFor = network, password = "", connectError = null)) }
        } else {
            join(network.ssid, null)
        }
    }

    fun setPassword(password: String) =
        _state.update { it.copy(add = it.add?.copy(password = password)) }

    fun dismissPassword() =
        _state.update { it.copy(add = it.add?.copy(passwordFor = null, password = "")) }

    fun submitPassword() {
        val add = _state.value.add ?: return
        val network = add.passwordFor ?: return
        join(network.ssid, add.password)
    }

    private fun join(ssid: String, password: String?) {
        // Keep passwordFor set for secured networks so a failure returns to the
        // password sheet (with the error) rather than the bare results list.
        _state.update {
            it.copy(add = it.add?.copy(connectingSsid = ssid, connectError = null))
        }
        viewModelScope.launch {
            try {
                wifi.connectWifi(ssid, password)
                val status = wifi.status()
                _state.update { it.copy(add = null, status = status) }
                toasts.show(
                    ToastData(
                        title = "Connected",
                        subtitle = "Joined \"$ssid\"",
                        tone = ToastTone.Success,
                    ),
                )
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                _state.update {
                    it.copy(
                        add = it.add?.copy(
                            connectingSsid = null,
                            connectError = e.message ?: "Couldn't join \"$ssid\".",
                        ),
                    )
                }
            }
        }
    }

    // ---- Lifecycle ----------------------------------------------------------

    override fun onCleared() {
        wifi.release()
        super.onCleared()
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

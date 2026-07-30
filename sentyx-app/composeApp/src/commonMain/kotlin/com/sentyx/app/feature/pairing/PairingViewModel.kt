package com.sentyx.app.feature.pairing

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.sentyx.app.core.permissions.BluetoothPermissionStatus
import com.sentyx.app.core.permissions.PermissionsController
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.domain.model.ConnectionTestStep
import com.sentyx.app.domain.model.DiscoveredDevice
import com.sentyx.app.domain.model.FirmwareUpdate
import com.sentyx.app.domain.model.HardwareRequirement
import com.sentyx.app.domain.model.OnboardingPermission
import com.sentyx.app.domain.model.ScanState
import com.sentyx.app.domain.model.WifiNetwork
import com.sentyx.app.domain.repository.PairingService
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/**
 * Single ViewModel driving the whole first-time pairing flow (intro → done).
 * Holds the shared UI state; each screen observes [state] and calls the action
 * for its step. Navigation is the screens' concern — this VM never touches a
 * Navigator.
 */
class PairingViewModel(
    private val pairing: PairingService,
    private val toasts: ToastController,
    private val permissionsController: PermissionsController,
) : ViewModel() {

    private val _state = MutableStateFlow(
        run {
            val initialStatus = permissionsController.status()
            PairingUiState(
                hardware = pairing.hardwareChecklist,
                permissions = pairing.permissions.map {
                    if (it.key == BLUETOOTH_PERMISSION_KEY) {
                        it.copy(granted = initialStatus == BluetoothPermissionStatus.Granted)
                    } else {
                        it
                    }
                },
                bluetoothStatus = initialStatus,
            )
        },
    )
    val state: StateFlow<PairingUiState> = _state.asStateFlow()

    private var scanJob: Job? = null
    private var testJob: Job? = null
    private var wifiLoadJob: Job? = null

    // ---- Permissions --------------------------------------------------------

    /**
     * Request a permission row. The Bluetooth row drives the real
     * [PermissionsController] (system prompt on Android; current CoreBluetooth
     * status on iOS) and reflects the outcome. Other onboarding permissions are
     * outside this BLE groundwork and are marked granted directly for now.
     */
    fun requestPermission(key: String) {
        val perm = _state.value.permissions.firstOrNull { it.key == key } ?: return
        if (perm.granted) return
        if (key == BLUETOOTH_PERMISSION_KEY) {
            viewModelScope.launch {
                val result = permissionsController.request()
                _state.update { s ->
                    s.copy(
                        permissions = s.permissions.map {
                            if (it.key == key) {
                                it.copy(granted = result == BluetoothPermissionStatus.Granted)
                            } else {
                                it
                            }
                        },
                        bluetoothStatus = result,
                        bluetoothRequested = true,
                    )
                }
            }
        } else {
            _state.update { s ->
                s.copy(permissions = s.permissions.map { if (it.key == key) it.copy(granted = true) else it })
            }
        }
    }

    // ---- Scan ---------------------------------------------------------------

    /** Start (or restart) the BLE scan; safe to call again for "Scan again". */
    fun startScan() {
        scanJob?.cancel()
        _state.update { it.copy(scan = ScanState.Scanning) }
        scanJob = viewModelScope.launch {
            pairing.scan().collect { scan ->
                _state.update { it.copy(scan = scan) }
            }
        }
    }

    // ---- Pairing ------------------------------------------------------------

    /**
     * Connect to [device] and authenticate (Just Works BLE bonding). On success
     * invokes [onSuccess] (the screen navigates to naming); on failure the
     * inline [PairingUiState.pairingError] carries the message so the user can
     * retry from the device list.
     */
    fun beginPairing(device: DiscoveredDevice, onSuccess: () -> Unit) {
        if (_state.value.pairing) return
        _state.update { it.copy(selectedDevice = device, pairing = true, pairingError = null) }
        viewModelScope.launch {
            try {
                pairing.beginPairing(device)
                onSuccess()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                _state.update {
                    it.copy(pairingError = e.message ?: "Couldn't pair with that device. Try again.")
                }
            } finally {
                _state.update { it.copy(pairing = false) }
            }
        }
    }

    // ---- Name / vehicle -----------------------------------------------------

    fun setDeviceName(name: String) = _state.update { it.copy(deviceName = name) }
    fun setNickname(nickname: String) = _state.update { it.copy(nickname = nickname) }

    /**
     * Register the device with the backend and push its config over BLE. Only
     * navigates ([onSuccess]) once that round-trip succeeds; on failure the
     * inline [PairingUiState.configureError] lets the user retry rather than
     * advancing into a half-provisioned flow.
     */
    fun configure(onSuccess: () -> Unit) {
        if (_state.value.configuring) return
        val s = _state.value
        _state.update { it.copy(configuring = true, configureError = null) }
        viewModelScope.launch {
            try {
                pairing.configure(
                    deviceName = s.deviceName,
                    vehicleModel = s.vehicleModel,
                    nickname = s.nickname.ifBlank { null },
                    timezone = s.timezone,
                )
                onSuccess()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                _state.update {
                    it.copy(configureError = e.message ?: "Couldn't set up the device. Try again.")
                }
            } finally {
                _state.update { it.copy(configuring = false) }
            }
        }
    }

    // ---- Wi-Fi --------------------------------------------------------------

    /**
     * Load visible networks and auto-select the first one (matching the design).
     * Single-flight: the device processes one Wi-Fi command at a time and rejects
     * an overlapping one (seen in the field as GATT error 128 when a recomposition
     * re-fired the load 60 ms after the first), so a load already in progress wins.
     */
    fun loadNetworks() {
        if (wifiLoadJob?.isActive == true) return
        wifiLoadJob = viewModelScope.launch {
            // A building broadcasts the same SSID from many APs; the device
            // reports them all. One row per SSID — the first (strongest, the
            // device orders by signal) wins.
            val nets = pairing.availableNetworks().distinctBy { it.ssid }
            _state.update { s ->
                val first = nets.firstOrNull()
                s.copy(
                    wifiNetworks = nets,
                    selectedSsid = s.selectedSsid ?: first?.ssid,
                    wifiPassword = if (s.wifiPassword.isEmpty() && first?.requiresPassword == true) {
                        PREFILLED_WIFI_PASSWORD
                    } else {
                        s.wifiPassword
                    },
                )
            }
        }
    }

    fun selectNetwork(ssid: String) {
        _state.update { s ->
            val net = s.wifiNetworks.firstOrNull { it.ssid == ssid }
            s.copy(
                selectedSsid = ssid,
                wifiPassword = if (net?.requiresPassword == true && s.wifiPassword.isEmpty()) {
                    PREFILLED_WIFI_PASSWORD
                } else {
                    s.wifiPassword
                },
            )
        }
    }

    fun setWifiPassword(password: String) = _state.update { it.copy(wifiPassword = password) }

    fun connectWifi() {
        val s = _state.value
        // The Connect button is disabled until a network is selected, so a null
        // here is a UI wiring bug — keep it loud.
        val ssid = s.selectedSsid ?: error("connectWifi called with no selected network")
        // The selected network CAN legitimately vanish from the list (a rescan
        // between selection and tap); wifi ops are fire-and-forget by design, so
        // fall back to sending the password we have rather than crashing — the
        // connection test surfaces any real failure.
        val net = s.wifiNetworks.firstOrNull { it.ssid == ssid }
        val password = if (net == null || net.requiresPassword) s.wifiPassword else null
        viewModelScope.launch {
            pairing.connectWifi(ssid, password?.ifBlank { null })
        }
    }

    // ---- Connection test ----------------------------------------------------

    /** Run the connection test; rows are revealed as each check "completes". */
    fun runConnectionTest() {
        testJob?.cancel()
        _state.update { it.copy(connectionTest = emptyList(), testRunning = true) }
        testJob = viewModelScope.launch {
            try {
                val steps = pairing.runConnectionTest()
                for (step in steps) {
                    delay(220)
                    _state.update { it.copy(connectionTest = it.connectionTest + step) }
                }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                _state.update {
                    it.copy(
                        connectionTest = it.connectionTest + ConnectionTestStep(
                            title = "Connection test failed",
                            subtitle = e.message ?: "Couldn't reach the server.",
                            passed = false,
                        ),
                    )
                }
            } finally {
                _state.update { it.copy(testRunning = false) }
            }
        }
    }

    // ---- Firmware / finish --------------------------------------------------

    fun loadFirmware() {
        viewModelScope.launch {
            val fw = pairing.requiredFirmwareUpdate()
            _state.update { it.copy(firmware = fw) }
        }
    }

    fun completePairing() {
        viewModelScope.launch {
            try {
                pairing.completePairing()
            } catch (e: CancellationException) {
                throw e
            } catch (_: Throwable) {
                // The device reboots its agent right after 'complete', so a
                // dropped connection here is expected and harmless — onboarding
                // is already persisted server- and device-side.
            }
        }
    }

    private companion object {
        /** Mock, editable placeholder so the password field shows dots by default. */
        const val PREFILLED_WIFI_PASSWORD = "sentyx-demo-key"

        /** Key of the Bluetooth onboarding permission (see DemoPairingService). */
        const val BLUETOOTH_PERMISSION_KEY = "ble"
    }
}

/** Immutable UI state for the entire pairing flow. */
data class PairingUiState(
    val hardware: List<HardwareRequirement>,
    val permissions: List<OnboardingPermission>,
    /** Real Bluetooth permission status from [PermissionsController]. */
    val bluetoothStatus: BluetoothPermissionStatus = BluetoothPermissionStatus.NotDetermined,
    /** True once the user has tapped to request Bluetooth (distinguishes iOS
     * "not determined, prompt comes later" from the untouched initial state). */
    val bluetoothRequested: Boolean = false,
    val scan: ScanState = ScanState.Scanning,
    val selectedDevice: DiscoveredDevice? = null,
    /** True while [beginPairing] is connecting/authenticating a device. */
    val pairing: Boolean = false,
    /** Inline error shown when connecting/authenticating a device failed. */
    val pairingError: String? = null,
    val deviceName: String = "Garage Pi",
    val vehicleModel: String = "Model 3",
    val nickname: String = "",
    val timezone: String = "Pacific · GMT-7",
    /** True while [configure] registers the device and pushes its config. */
    val configuring: Boolean = false,
    /** Inline error shown when device registration/config push failed. */
    val configureError: String? = null,
    val wifiNetworks: List<WifiNetwork> = emptyList(),
    val selectedSsid: String? = null,
    val wifiPassword: String = "",
    val connectionTest: List<ConnectionTestStep> = emptyList(),
    val testRunning: Boolean = false,
    val firmware: FirmwareUpdate? = null,
) {
    /**
     * Whether the permissions step may continue: Bluetooth granted, or (iOS) the
     * user tapped and the status is still "not determined" because the system
     * prompt only appears on first real BLE use.
     */
    val canProceedFromPermissions: Boolean
        get() = bluetoothStatus == BluetoothPermissionStatus.Granted ||
            (bluetoothRequested && bluetoothStatus == BluetoothPermissionStatus.NotDetermined)
}

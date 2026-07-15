package com.sentyx.app.data.demo

import com.sentyx.app.domain.model.ConnectionTestStep
import com.sentyx.app.domain.model.DiscoveredDevice
import com.sentyx.app.domain.model.FirmwareUpdate
import com.sentyx.app.domain.model.HardwareRequirement
import com.sentyx.app.domain.model.OnboardingPermission
import com.sentyx.app.domain.model.ScanState
import com.sentyx.app.domain.model.WifiNetwork
import com.sentyx.app.domain.repository.PairingService
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow

/**
 * Demo [PairingService]. Drives the onboarding flow with the prototype's fixed
 * data: a single discoverable "Garage Pi", three Wi-Fi networks, a passing
 * connection test, and a required firmware update. completePairing marks the
 * device ready by resetting the demo scenario.
 */
class DemoPairingService(
    private val demoState: DemoStateController,
) : PairingService {

    override val hardwareChecklist: List<HardwareRequirement> = listOf(
        HardwareRequirement("🍓", "Sentyx Pi installed", "Connected to your Tesla USB port and powered on"),
        HardwareRequirement("📶", "Bluetooth on", "For first-time pairing and nearby control"),
        HardwareRequirement("🔑", "Your Sentyx account", "To link the Pi to the cloud backend"),
    )

    override val permissions: List<OnboardingPermission> = listOf(
        OnboardingPermission("ble", "ᔨ", "Bluetooth", "Discover and pair the Pi", true),
        OnboardingPermission("net", "🌐", "Local network", "Talk to the Pi over Wi-Fi", true),
        OnboardingPermission("notif", "🔔", "Notifications", "Alert you about events", false),
    )

    override fun scan(): Flow<ScanState> = flow {
        emit(ScanState.Scanning)
        delay(1500)
        emit(
            ScanState.Found(
                listOf(
                    DiscoveredDevice(
                        id = "garage-pi",
                        name = "Garage Pi",
                        subtitle = "Sentyx Pi · signal strong",
                    ),
                ),
            ),
        )
    }

    override suspend fun beginPairing(device: DiscoveredDevice) {
        delay(600)
    }

    override suspend fun configure(
        deviceName: String,
        vehicleModel: String,
        nickname: String?,
        timezone: String,
    ) {
        delay(500)
    }

    override suspend fun availableNetworks(): List<WifiNetwork> {
        delay(500)
        return listOf(
            WifiNetwork("Garage 5G", "Secured · strong", requiresPassword = true),
            WifiNetwork("Home", "Secured", requiresPassword = true),
            WifiNetwork("Neighbor_2.4", "Secured · weak", requiresPassword = true),
        )
    }

    override suspend fun connectWifi(ssid: String, password: String?) {
        delay(900)
    }

    override suspend fun runConnectionTest(): List<ConnectionTestStep> {
        delay(1200)
        return DemoDeviceRepository.CONNECTION_TEST_STEPS
    }

    override suspend fun requiredFirmwareUpdate(): FirmwareUpdate {
        delay(500)
        return DemoDeviceRepository.FIRMWARE_UPDATE
    }

    override suspend fun completePairing() {
        delay(500)
        demoState.deviceScenario.value = DeviceScenario.Normal
    }
}

package com.sentyx.app.domain.repository

import com.sentyx.app.domain.model.ConnectionTestStep
import com.sentyx.app.domain.model.DiscoveredDevice
import com.sentyx.app.domain.model.FirmwareUpdate
import com.sentyx.app.domain.model.HardwareRequirement
import com.sentyx.app.domain.model.OnboardingPermission
import com.sentyx.app.domain.model.ScanState
import com.sentyx.app.domain.model.WifiNetwork
import kotlinx.coroutines.flow.Flow

/** First-time device pairing: scan → connect → configure → test. */
interface PairingService {
    val hardwareChecklist: List<HardwareRequirement>
    val permissions: List<OnboardingPermission>

    /** Emits Scanning, then Found/NoneFound. */
    fun scan(): Flow<ScanState>

    /**
     * Connect to [device] and authenticate (Just Works BLE bonding); on return
     * the device is paired and ready to configure. Throws on conn(or auth) failure.
     */
    suspend fun beginPairing(device: DiscoveredDevice)

    suspend fun configure(deviceName: String, vehicleModel: String, nickname: String?, timezone: String)

    suspend fun availableNetworks(): List<WifiNetwork>
    suspend fun connectWifi(ssid: String, password: String?)

    suspend fun runConnectionTest(): List<ConnectionTestStep>

    /** Firmware update required to finish onboarding, or null. */
    suspend fun requiredFirmwareUpdate(): FirmwareUpdate?
    suspend fun completePairing()
}

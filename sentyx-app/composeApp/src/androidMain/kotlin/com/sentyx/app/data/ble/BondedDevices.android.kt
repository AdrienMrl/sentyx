package com.sentyx.app.data.ble

import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothManager
import android.content.Context
import android.os.ParcelUuid
import com.juul.kable.Peripheral
import com.sentyx.app.core.storage.SentyxAppContext

/**
 * Android bond table via [android.bluetooth.BluetoothAdapter.getBondedDevices].
 *
 * Matching is on the bonded device's service-UUID list, never on its name: the
 * name stored in a bond record is whatever the peripheral reported at bond time
 * (often the Pi's hostname rather than its advertised LocalName), so a name
 * match would be both lossy and prone to false positives.
 */
actual class BondedDevices actual constructor() {

    private val serviceUuid = ParcelUuid.fromString(SentyxGatt.SERVICE_UUID)

    actual fun sentyxPeripherals(): List<BondedPeripheral> =
        bonded()
            .filter { device -> device.uuids?.any { it == serviceUuid } == true }
            .map { device ->
                BondedPeripheral(
                    identifier = device.address,
                    name = device.name ?: "Sentyx Pi",
                )
            }

    actual fun peripheralFor(identifier: String): Peripheral {
        val device = bonded().firstOrNull { it.address == identifier }
            ?: error("No bonded Bluetooth device with address $identifier")
        return Peripheral(device)
    }

    /**
     * Bonded devices, or empty when Bluetooth is unavailable/off. Reading the
     * bond table needs BLUETOOTH_CONNECT on API 31+; if the user has not granted
     * it yet the SecurityException is swallowed so pairing falls back to a plain
     * advertisement scan rather than crashing.
     */
    private fun bonded(): Set<BluetoothDevice> {
        val manager = SentyxAppContext.require()
            .getSystemService(Context.BLUETOOTH_SERVICE) as? BluetoothManager
            ?: return emptySet()
        val adapter = manager.adapter ?: return emptySet()
        return try {
            if (!adapter.isEnabled) emptySet() else adapter.bondedDevices ?: emptySet()
        } catch (e: SecurityException) {
            emptySet()
        }
    }
}

package com.sentyx.app.data.ble

import com.juul.kable.Peripheral

/**
 * iOS has no queryable bond table: CoreBluetooth keeps pairing state inside the
 * system and exposes no equivalent of Android's `getBondedDevices`. The nearest
 * analogue is `retrievePeripherals(withIdentifiers:)`, which needs a previously
 * persisted CBPeripheral identifier rather than a bond list.
 *
 * So this reports nothing and iOS pairing keeps working exactly as before —
 * purely from live advertisements. Reviving the already-paired path here means
 * persisting the peripheral identifier at pair time and retrieving it by UUID;
 * until then [peripheralFor] is unreachable, because [sentyxPeripherals] is the
 * only source of the identifiers it accepts.
 */
actual class BondedDevices actual constructor() {

    actual fun sentyxPeripherals(): List<BondedPeripheral> = emptyList()

    actual fun peripheralFor(identifier: String): Peripheral =
        error("iOS exposes no bond table; no bonded peripheral for $identifier")
}

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

    /**
     * iOS keeps pairing state entirely inside the system: there is no API to
     * drop it, so this always reports failure and callers fall back to telling
     * the user to forget the device in Settings — which on iOS does list it.
     */
    actual fun removeBond(identifier: String): Boolean = false
}

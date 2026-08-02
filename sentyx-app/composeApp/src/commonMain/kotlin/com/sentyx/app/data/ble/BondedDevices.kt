package com.sentyx.app.data.ble

import com.juul.kable.Peripheral

/** A Sentyx peripheral the OS already holds a bond for. */
data class BondedPeripheral(val identifier: String, val name: String)

/**
 * The platform's bond table, filtered to peripherals exposing
 * [SentyxGatt.SERVICE_UUID].
 *
 * Why this exists: [BlePairingService.scan] can only offer what its
 * advertisement filter matched, so a known Pi whose advertisement was missed
 * left the user with an empty list and no way forward but to "forget" the
 * device in system Bluetooth settings. Misses are real on this stack — the
 * legacy btmgmt advertising fallback splits the service UUID (advertising PDU)
 * from the LocalName (scan response), and scan-response data does not always
 * arrive in the same pass — so a previously bonded device is offered directly
 * from the bond table instead of depending on a fresh advertisement.
 *
 * This does NOT rescue a Pi that has stopped advertising altogether: BLE
 * requires a peripheral to be in connectable advertising state before any
 * central can connect, so a silent Pi is unreachable by every path, bonded or
 * not. The agent advertises for its whole lifetime (see `blepair.go`, which
 * even re-asserts a dropped instance via watchdog), so silence there is a Pi
 * fault to fix on the Pi — not something the phone can work around.
 */
expect class BondedDevices() {

    /**
     * Bonded peripherals advertising the Sentyx service, or empty when the
     * adapter is off/absent or the Bluetooth permission is not granted.
     */
    fun sentyxPeripherals(): List<BondedPeripheral>

    /**
     * A connectable peripheral for [identifier], which must come from
     * [sentyxPeripherals]. Throws if the bond has since been removed.
     */
    fun peripheralFor(identifier: String): Peripheral

    /**
     * Drop this phone's bond for [identifier]; true when it is gone afterwards.
     *
     * A unit that has been reflashed no longer holds the keys this phone kept,
     * so every encrypted operation fails and onboarding cannot start. The bond
     * was created by this app, is invisible in Android's Bluetooth settings, and
     * the user has no way to reach it — telling them to go and remove it was
     * asking for something that could not be done. Clearing it here is the only
     * path that leaves them somewhere other than stuck.
     */
    fun removeBond(identifier: String): Boolean
}

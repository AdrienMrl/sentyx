package com.sentyx.app.core.permissions

import platform.CoreBluetooth.CBManager
import platform.CoreBluetooth.CBManagerAuthorizationAllowedAlways
import platform.CoreBluetooth.CBManagerAuthorizationDenied
import platform.CoreBluetooth.CBManagerAuthorizationNotDetermined
import platform.CoreBluetooth.CBManagerAuthorizationRestricted

/**
 * iOS [PermissionsController] reading CoreBluetooth's authorization status.
 *
 * iOS has no standalone "request Bluetooth" prompt: the system alert appears the
 * first time a `CBCentralManager` is created and starts scanning (that happens in
 * the later BLE task). So [request] just reports the current status —
 * [BluetoothPermissionStatus.NotDetermined] means the UI can proceed and the
 * prompt will fire on first real BLE use.
 */
class IosPermissionsController : PermissionsController {

    override fun status(): BluetoothPermissionStatus = when (CBManager.authorization) {
        CBManagerAuthorizationAllowedAlways -> BluetoothPermissionStatus.Granted
        CBManagerAuthorizationDenied, CBManagerAuthorizationRestricted -> BluetoothPermissionStatus.Denied
        CBManagerAuthorizationNotDetermined -> BluetoothPermissionStatus.NotDetermined
        else -> BluetoothPermissionStatus.NotDetermined
    }

    override suspend fun request(): BluetoothPermissionStatus = status()
}

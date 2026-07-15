package com.sentyx.app.core.permissions

import androidx.compose.runtime.staticCompositionLocalOf

/** Outcome of a Bluetooth permission check or request. */
enum class BluetoothPermissionStatus {
    /** The user has granted the permission group. */
    Granted,

    /** The user explicitly denied (or the OS restricts) the permission. */
    Denied,

    /** Not yet decided — no prompt has been answered. On iOS, proceeding is fine;
     * the OS prompt fires the first time the central manager starts. */
    NotDetermined,
}

/**
 * Platform bridge for the runtime Bluetooth permission group used to scan for
 * and connect to a Sentyx Pi. The Android implementation is backed by the
 * Activity Result API (registered in `MainActivity`); the iOS implementation
 * reads CoreBluetooth's authorization status. Provided to common UI through
 * [LocalPermissionsController].
 */
interface PermissionsController {
    /** Current status without prompting the user. */
    fun status(): BluetoothPermissionStatus

    /** Request the permission group, suspending until the user responds (or,
     * on iOS, returning the current status immediately). */
    suspend fun request(): BluetoothPermissionStatus
}

/**
 * Inert controller used as the [LocalPermissionsController] default so previews
 * and tests compose without a platform host. Reports [BluetoothPermissionStatus.NotDetermined];
 * real behavior comes from the platform controller each entry point provides.
 */
object NoopPermissionsController : PermissionsController {
    override fun status(): BluetoothPermissionStatus = BluetoothPermissionStatus.NotDetermined
    override suspend fun request(): BluetoothPermissionStatus = BluetoothPermissionStatus.NotDetermined
}

/** Provided by each platform entry point (Android `MainActivity`, iOS `MainViewController`). */
val LocalPermissionsController = staticCompositionLocalOf<PermissionsController> { NoopPermissionsController }

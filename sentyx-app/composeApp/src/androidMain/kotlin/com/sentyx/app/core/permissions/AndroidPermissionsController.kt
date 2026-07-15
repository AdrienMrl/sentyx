package com.sentyx.app.core.permissions

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import androidx.activity.ComponentActivity
import androidx.activity.result.contract.ActivityResultContracts
import kotlin.coroutines.resume
import kotlinx.coroutines.suspendCancellableCoroutine

/**
 * Android [PermissionsController] backed by the Activity Result API. The
 * launcher must be registered before the Activity is STARTED, so this is
 * constructed from `MainActivity.onCreate`.
 *
 * On API 31+ it requests the runtime `BLUETOOTH_SCAN` + `BLUETOOTH_CONNECT`
 * group; on API 24-30 (where those don't exist) it requests `ACCESS_FINE_LOCATION`,
 * which legacy BLE scanning requires. The legacy `BLUETOOTH` / `BLUETOOTH_ADMIN`
 * permissions are normal (install-time) and declared in the manifest only.
 */
class AndroidPermissionsController(
    private val activity: ComponentActivity,
) : PermissionsController {

    private val required: Array<String> =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            arrayOf(
                Manifest.permission.BLUETOOTH_SCAN,
                Manifest.permission.BLUETOOTH_CONNECT,
            )
        } else {
            arrayOf(Manifest.permission.ACCESS_FINE_LOCATION)
        }

    /** Continuation resolver for the in-flight [request], if any. */
    private var pending: ((BluetoothPermissionStatus) -> Unit)? = null

    private val launcher = activity.registerForActivityResult(
        ActivityResultContracts.RequestMultiplePermissions(),
    ) { grants ->
        val granted = required.all { grants[it] == true }
        pending?.invoke(
            if (granted) BluetoothPermissionStatus.Granted else BluetoothPermissionStatus.Denied,
        )
        pending = null
    }

    override fun status(): BluetoothPermissionStatus {
        val granted = required.all {
            activity.checkSelfPermission(it) == PackageManager.PERMISSION_GRANTED
        }
        return if (granted) BluetoothPermissionStatus.Granted else BluetoothPermissionStatus.NotDetermined
    }

    override suspend fun request(): BluetoothPermissionStatus {
        if (status() == BluetoothPermissionStatus.Granted) return BluetoothPermissionStatus.Granted
        return suspendCancellableCoroutine { cont ->
            pending = { cont.resume(it) }
            launcher.launch(required)
        }
    }
}

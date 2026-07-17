package com.sentyx.app.core.permissions

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import androidx.activity.ComponentActivity
import androidx.activity.result.contract.ActivityResultContracts
import kotlin.coroutines.resume
import kotlinx.coroutines.suspendCancellableCoroutine

/**
 * Android [NotificationPermissionController] backed by the Activity Result API.
 * The launcher must be registered before the Activity is STARTED, so this is
 * constructed from `MainActivity.onCreate`.
 *
 * On API 33+ it requests the runtime `POST_NOTIFICATIONS` permission; below 33
 * the permission does not exist and notifications are allowed by default, so
 * [ensurePermission] returns true without prompting.
 */
class AndroidNotificationPermissionController(
    private val activity: ComponentActivity,
) : NotificationPermissionController {

    /** Continuation resolver for the in-flight request, if any. */
    private var pending: ((Boolean) -> Unit)? = null

    private val launcher = activity.registerForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { granted ->
        pending?.invoke(granted)
        pending = null
    }

    override suspend fun ensurePermission(): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return true
        val alreadyGranted = activity.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED
        if (alreadyGranted) return true
        return suspendCancellableCoroutine { cont ->
            pending = { cont.resume(it) }
            launcher.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
    }
}

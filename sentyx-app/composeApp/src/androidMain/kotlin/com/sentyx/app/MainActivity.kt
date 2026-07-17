package com.sentyx.app

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.tooling.preview.Preview
import com.sentyx.app.core.permissions.AndroidNotificationPermissionController
import com.sentyx.app.core.permissions.AndroidPermissionsController
import com.sentyx.app.core.permissions.LocalNotificationPermissionController
import com.sentyx.app.core.permissions.LocalPermissionsController
import com.sentyx.app.core.permissions.NotificationPermissionController
import com.sentyx.app.core.permissions.PermissionsController
import com.sentyx.app.core.push.NotificationChannels
import com.sentyx.app.core.storage.SentyxAppContext

class MainActivity : ComponentActivity() {

    // Registers the Activity Result launchers; must be built before STARTED.
    private lateinit var permissions: PermissionsController
    private lateinit var notificationPermissions: NotificationPermissionController

    override fun onCreate(savedInstanceState: Bundle?) {
        // Populate the process Context for commonMain platform stores (KeyValueStore)
        // before the app container is created inside setContent.
        SentyxAppContext.init(this)
        permissions = AndroidPermissionsController(this)
        notificationPermissions = AndroidNotificationPermissionController(this)
        // Create the Sentry-alerts channel on app start so backgrounded FCM
        // notification messages display on the high-importance channel.
        NotificationChannels.ensureSentryAlerts(this)
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)

        setContent {
            CompositionLocalProvider(
                LocalPermissionsController provides permissions,
                LocalNotificationPermissionController provides notificationPermissions,
            ) {
                App()
            }
        }
    }
}

@Preview
@Composable
private fun AppAndroidPreview() {
    App()
}

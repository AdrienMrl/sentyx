package com.sentyx.app

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.tooling.preview.Preview
import com.sentyx.app.core.permissions.AndroidPermissionsController
import com.sentyx.app.core.permissions.LocalPermissionsController
import com.sentyx.app.core.permissions.PermissionsController

class MainActivity : ComponentActivity() {

    // Registers the Activity Result launcher; must be built before STARTED.
    private lateinit var permissions: PermissionsController

    override fun onCreate(savedInstanceState: Bundle?) {
        permissions = AndroidPermissionsController(this)
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)

        setContent {
            CompositionLocalProvider(LocalPermissionsController provides permissions) {
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

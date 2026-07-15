package com.sentyx.app

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.window.ComposeUIViewController
import com.sentyx.app.core.permissions.IosPermissionsController
import com.sentyx.app.core.permissions.LocalPermissionsController

fun MainViewController() = ComposeUIViewController {
    CompositionLocalProvider(LocalPermissionsController provides IosPermissionsController()) {
        App()
    }
}

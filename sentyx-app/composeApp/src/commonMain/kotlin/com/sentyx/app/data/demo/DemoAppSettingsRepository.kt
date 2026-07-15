package com.sentyx.app.data.demo

import com.sentyx.app.domain.model.AppPrefs
import com.sentyx.app.domain.model.PermissionState
import com.sentyx.app.domain.model.PermissionStatus
import com.sentyx.app.domain.repository.AppSettingsRepository
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/** Demo [AppSettingsRepository]: static app preferences + phone permission status. */
class DemoAppSettingsRepository : AppSettingsRepository {

    override val prefs: StateFlow<AppPrefs> = MutableStateFlow(
        AppPrefs(units = "Imperial", timeDisplay = "12-hour", language = "English"),
    )

    override val permissions: StateFlow<List<PermissionStatus>> = MutableStateFlow(
        listOf(
            PermissionStatus("Bluetooth", PermissionState.Allowed),
            PermissionStatus("Local network", PermissionState.Allowed),
            PermissionStatus("Notifications", PermissionState.Denied),
            PermissionStatus("Photo library", PermissionState.Allowed),
            PermissionStatus("Files", PermissionState.Ask),
        ),
    )
}

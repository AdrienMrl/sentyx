package com.sentyx.app.feature.settings

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxListGroup
import com.sentyx.app.core.designsystem.SxListRow

/**
 * App settings: a preferences group (units, time display, language) reflecting
 * [AppSettingsRepository.prefs], plus a support group. Every row is a mock toast.
 */
@Composable
fun AppSettingsScreen(
    vm: AppSettingsViewModel,
    onBack: () -> Unit,
) {
    val prefs by vm.prefs.collectAsState()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(start = 22.dp, end = 22.dp, top = 56.dp, bottom = 40.dp),
    ) {
        SxBackHeader("App settings", onBack, modifier = Modifier.padding(top = 8.dp))

        SxListGroup(modifier = Modifier.padding(top = 18.dp)) {
            SxListRow("Units", value = prefs.units, showChevron = true, onClick = vm::stub)
            SxListRow("Time display", value = prefs.timeDisplay, showChevron = true, onClick = vm::stub)
            SxListRow("Language", value = prefs.language, showChevron = true, showDivider = false, onClick = vm::stub)
        }

        SxListGroup(modifier = Modifier.padding(top = 14.dp)) {
            SxListRow("Help center", showChevron = true, onClick = vm::stub)
            SxListRow("Contact support", showChevron = true, onClick = vm::stub)
            SxListRow("Send feedback", showChevron = true, showDivider = false, onClick = vm::stub)
        }
    }
}

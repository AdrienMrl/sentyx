package com.sentyx.app.feature.transfers

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxListGroup
import com.sentyx.app.core.designsystem.SxListRow
import com.sentyx.app.core.designsystem.SxSectionHeader
import com.sentyx.app.core.designsystem.SxToggleRow
import com.sentyx.app.domain.model.TransferRoute

/**
 * Transfer preferences: the preferred download route and the automatic
 * upload/download toggles, all bound to [com.sentyx.app.domain.model.TransferSettings].
 */
@Composable
fun TransferSettingsScreen(
    vm: TransfersViewModel,
    onBack: () -> Unit,
) {
    val state by vm.uiState.collectAsStateWithLifecycle()
    val settings = state.settings
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(top = 56.dp, bottom = 40.dp)
            .padding(horizontal = 22.dp),
    ) {
        SxBackHeader(title = "Transfer settings", onBack = onBack, modifier = Modifier.padding(top = 8.dp))

        SxSectionHeader("Preferred route")
        SxListGroup {
            val routes = state.routeAvailability
            routes.forEachIndexed { index, (route, available) ->
                val selected = route == settings.preferredRoute
                SxListRow(
                    label = routeLabel(route),
                    labelColor = when {
                        !available -> SxColors.Faint
                        selected -> SxColors.Ink
                        else -> SxColors.InkSecondary
                    },
                    showDivider = index != routes.lastIndex,
                    onClick = if (available) {
                        { vm.selectRoute(route) }
                    } else {
                        null
                    },
                    trailing = if (selected) {
                        { Text("✓", color = SxColors.Green, fontSize = 14.sp, fontWeight = FontWeight.Bold) }
                    } else {
                        null
                    },
                )
            }
        }

        SxSectionHeader("Automatic")
        SxListGroup {
            SxToggleRow(
                title = "Auto-upload new clips",
                subtitle = "Pi uploads when on Wi-Fi",
                checked = settings.autoUploadNewClips,
                onToggle = vm::setAutoUploadNewClips,
                modifier = Modifier.fillMaxWidth(),
            )
            RowDivider()
            SxToggleRow(
                title = "Only most-relevant camera",
                subtitle = "Upload one angle to save data",
                checked = settings.onlyMostRelevantCamera,
                onToggle = vm::setOnlyMostRelevantCamera,
                modifier = Modifier.fillMaxWidth(),
            )
            RowDivider()
            SxToggleRow(
                title = "Allow cellular downloads",
                subtitle = "Backend clips over cellular data",
                checked = settings.allowCellularDownloads,
                onToggle = vm::setAllowCellularDownloads,
                modifier = Modifier.fillMaxWidth(),
            )
        }
    }
}

@Composable
private fun RowDivider() {
    Box(
        Modifier
            .fillMaxWidth()
            .height(1.dp)
            .background(SxColors.Divider),
    )
}

/** Display label for the preferred-route list (matches the design copy). */
private fun routeLabel(route: TransferRoute): String = when (route) {
    TransferRoute.WifiDirect -> "Direct Wi-Fi"
    TransferRoute.WifiLocal -> "Home Wi-Fi network"
    TransferRoute.Bluetooth -> "Bluetooth"
}

package com.sentyx.app.feature.device

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxListGroup
import com.sentyx.app.core.designsystem.SxListRow
import com.sentyx.app.core.designsystem.SxSectionHeader

/**
 * Device settings & maintenance: identity, behavior, maintenance, and a
 * red-tinted danger group. Identity/behavior rows are mock toasts; maintenance
 * routes to firmware/diagnostics, and the danger group to remove/factory reset.
 */
@Composable
fun DeviceSettingsScreen(
    vm: DeviceViewModel,
    onBack: () -> Unit,
    onOpenFirmware: () -> Unit,
    onOpenDiagnostics: () -> Unit,
    onOpenWifi: () -> Unit,
    onFactoryReset: () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(top = 56.dp, bottom = 40.dp)
            .padding(horizontal = 22.dp),
    ) {
        SxBackHeader(title = "Device settings", onBack = onBack, modifier = Modifier.padding(top = 8.dp))

        SxSectionHeader("Identity")
        SxListGroup {
            SxListRow(label = "Device name", value = "Garage Pi", showChevron = true, onClick = { vm.comingSoon("Device name") })
            SxListRow(label = "Vehicle & timezone", value = "Model 3 · GMT-7", showChevron = true, onClick = { vm.comingSoon("Vehicle & timezone") })
            SxListRow(label = "Saved Wi-Fi networks", showChevron = true, showDivider = false, onClick = onOpenWifi)
        }

        SxSectionHeader("Behavior")
        SxListGroup {
            SxListRow(label = "Clip selection", value = "Most relevant", showChevron = true, onClick = { vm.comingSoon("Clip selection") })
            SxListRow(label = "Local retention", value = "7 days", showChevron = true, onClick = { vm.comingSoon("Local retention") })
            SxListRow(label = "Offline spool limit", value = "4 GB", showChevron = true, showDivider = false, onClick = { vm.comingSoon("Offline spool limit") })
        }

        SxSectionHeader("Maintenance")
        SxListGroup {
            SxListRow(label = "Firmware update", showChevron = true, onClick = onOpenFirmware)
            SxListRow(label = "Diagnostics & connection test", showChevron = true, onClick = onOpenDiagnostics)
            SxListRow(label = "Restart device", showChevron = true, showDivider = false, onClick = { vm.restart() })
        }

        DangerGroup(
            onRemove = { vm.remove() },
            onFactoryReset = onFactoryReset,
            modifier = Modifier.padding(top = 20.dp),
        )
    }
}

@Composable
private fun DangerGroup(
    onRemove: () -> Unit,
    onFactoryReset: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val shape = RoundedCornerShape(14.dp)
    Column(
        modifier = modifier
            .clip(shape)
            .background(SxColors.Card)
            .border(1.dp, Color(0xFFF0D9CE), shape),
    ) {
        SxListRow(
            label = "Remove device",
            labelColor = SxColors.Red,
            showChevron = true,
            onClick = onRemove,
        )
        SxListRow(
            label = "Factory reset",
            labelColor = SxColors.Red,
            showChevron = true,
            showDivider = false,
            onClick = onFactoryReset,
        )
    }
}

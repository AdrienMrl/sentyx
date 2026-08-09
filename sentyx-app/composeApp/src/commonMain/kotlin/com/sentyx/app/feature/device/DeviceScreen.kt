package com.sentyx.app.feature.device

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.PulsingDot
import com.sentyx.app.core.designsystem.SxCard
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxEmptyState
import com.sentyx.app.core.designsystem.SxListGroup
import com.sentyx.app.core.designsystem.SxListRow
import com.sentyx.app.core.designsystem.SxPrimaryButton
import com.sentyx.app.core.designsystem.SxProgressBar
import com.sentyx.app.core.designsystem.SxSecondaryButton
import com.sentyx.app.core.designsystem.SxSectionHeader
import com.sentyx.app.domain.model.DeviceCondition
import com.sentyx.app.domain.model.DeviceSnapshot
import kotlin.math.roundToInt

/** Label + dot/text colors for the status line, derived from [DeviceCondition]. */
private data class ConditionStyle(val label: String, val dot: Color, val fg: Color)

private fun conditionStyle(condition: DeviceCondition): ConditionStyle = when (condition) {
    DeviceCondition.Online -> ConditionStyle("Online", SxColors.Green, SxColors.Green)
    DeviceCondition.Offline -> ConditionStyle("Offline", SxColors.Red, SxColors.Red)
    DeviceCondition.NeedsAttention -> ConditionStyle("Needs attention", SxColors.Amber, SxColors.AmberDeep)
    DeviceCondition.UpdateRequired -> ConditionStyle("Update required", SxColors.Amber, SxColors.AmberDeep)
    DeviceCondition.LocalOnly -> ConditionStyle("Local only", SxColors.Amber, SxColors.AmberDeep)
}

/**
 * Device tab root: name + status, optional issue banner, connections, storage,
 * a 2x2 health grid, and maintenance CTAs.
 */
@Composable
fun DeviceScreen(
    vm: DeviceViewModel,
    onOpenSettings: () -> Unit,
    onOpenFirmware: () -> Unit,
    onAddDevice: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()
    val snapshot = state.snapshot

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(top = 56.dp, bottom = 96.dp)
            .padding(horizontal = 22.dp),
    ) {
        if (snapshot == null) {
            SxEmptyState(
                emoji = "📶",
                title = "No device paired",
                body = "Pair a Sentyx Pi to see its status and health here.",
                modifier = Modifier.fillMaxWidth().padding(top = 40.dp),
            )
            SxPrimaryButton(text = "Add a device", onClick = onAddDevice)
            return@Column
        }

        DeviceHeader(name = snapshot.name, onAddDevice = onAddDevice)
        StatusLine(snapshot)

        if (snapshot.issues.isNotEmpty()) {
            IssueBanner(snapshot.issues)
        }

        SxSectionHeader("Connections")
        ConnectionsGroup(snapshot)

        SxSectionHeader("Storage")
        StorageCard(snapshot)

        SxSectionHeader("Health")
        HealthGrid(snapshot)

        if (snapshot.firmwareUpdateAvailable) {
            // Reads as navigation, not as the install itself: the flag also
            // covers an update already downloading, waiting for the car to stop
            // recording, or one that failed — the firmware screen says which.
            Box(Modifier.padding(top = 12.dp)) {
                SxPrimaryButton(text = "Firmware update", onClick = onOpenFirmware)
            }
        }
        Box(Modifier.padding(top = 12.dp)) {
            SxSecondaryButton(text = "Device settings & maintenance", onClick = onOpenSettings)
        }
    }
}

@Composable
private fun DeviceHeader(name: String, onAddDevice: () -> Unit) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(top = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.SpaceBetween,
    ) {
        Text(
            name,
            color = SxColors.Ink,
            fontSize = 26.sp,
            fontWeight = FontWeight.ExtraBold,
            style = TextStyle(letterSpacing = (-0.5).sp),
            modifier = Modifier.weight(1f, fill = false),
        )
        Box(
            modifier = Modifier
                .size(34.dp)
                .clip(CircleShape)
                .background(SxColors.Card)
                .border(1.dp, SxColors.Border, CircleShape)
                .clickable(onClick = onAddDevice),
            contentAlignment = Alignment.Center,
        ) {
            Text("+", color = SxColors.Ink, fontSize = 18.sp)
        }
    }
}

@Composable
private fun StatusLine(snapshot: DeviceSnapshot) {
    val style = conditionStyle(snapshot.condition)
    Row(
        modifier = Modifier.fillMaxWidth().padding(top = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(7.dp),
    ) {
        PulsingDot(color = style.dot, size = 9.dp)
        Text(style.label, color = style.fg, fontSize = 13.sp, fontWeight = FontWeight.Bold)
        Text(
            "· ${snapshot.vehicleModel} · last seen ${snapshot.lastSeen}",
            color = SxColors.Muted,
            fontSize = 12.5.sp,
        )
    }
}

@Composable
private fun IssueBanner(issues: List<String>) {
    val shape = RoundedCornerShape(14.dp)
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(top = 16.dp)
            .clip(shape)
            .background(SxColors.Amber.copy(alpha = 0.10f))
            .border(1.dp, SxColors.Amber.copy(alpha = 0.28f), shape)
            .padding(horizontal = 14.dp, vertical = 13.dp),
        verticalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        for (issue in issues) {
            Row(horizontalArrangement = Arrangement.spacedBy(9.dp)) {
                Text("⚠", color = SxColors.Amber, fontSize = 13.sp)
                Text(issue, color = Color(0xFF6E4E1A), fontSize = 13.sp)
            }
        }
    }
}

@Composable
private fun ConnectionsGroup(snapshot: DeviceSnapshot) {
    val bleColor = if (snapshot.bleConnected) SxColors.Green else SxColors.Red
    val bleText = if (snapshot.bleConnected) "Connected" else "Off"

    val wifiColor = if (snapshot.wifiConnected) SxColors.Green else SxColors.Red
    val wifiText = if (snapshot.wifiConnected) {
        val net = snapshot.wifiNetwork
        if (net != null) "Connected · $net" else "Connected"
    } else {
        "Disconnected"
    }

    val backendColor = if (snapshot.backendConnected) SxColors.Green else SxColors.Amber
    val backendText = if (snapshot.backendConnected) "Connected" else "Unreachable"

    SxListGroup {
        SxListRow(label = "Bluetooth", value = bleText, valueColor = bleColor)
        SxListRow(label = "Wi-Fi", value = wifiText, valueColor = wifiColor)
        SxListRow(label = "Sentyx backend", value = backendText, valueColor = backendColor, showDivider = false)
    }
}

@Composable
private fun StorageCard(snapshot: DeviceSnapshot) {
    val usedFraction = (100 - snapshot.storageFreePct).coerceIn(0, 100) / 100f
    val offlineCapacity = (snapshot.storageFreePct * 0.9).roundToInt()
    val backlogText = if (snapshot.uploadBacklog > 0) "${snapshot.uploadBacklog} clips queued" else "None"
    val writingText = if (snapshot.recordingNow) "Recording an event now" else "Idle"

    SxCard {
        Column(Modifier.fillMaxWidth()) {
            Row(
                modifier = Modifier.fillMaxWidth().padding(bottom = 9.dp),
                horizontalArrangement = Arrangement.SpaceBetween,
            ) {
                Text("${snapshot.storageFreePct}% free", color = SxColors.Ink, fontSize = 13.sp, fontWeight = FontWeight.Bold)
                Text("~$offlineCapacity more events offline", color = SxColors.Muted, fontSize = 12.sp)
            }
            SxProgressBar(pct = usedFraction, color = SxColors.Bronze, height = 8.dp)
            StorageRow(label = "Upload backlog", value = backlogText, topPadding = 11.dp)
            StorageRow(label = "Currently", value = writingText, topPadding = 7.dp)
        }
    }
}

@Composable
private fun StorageRow(label: String, value: String, topPadding: androidx.compose.ui.unit.Dp) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(top = topPadding),
        horizontalArrangement = Arrangement.SpaceBetween,
    ) {
        Text(label, color = SxColors.Muted, fontSize = 12.5.sp)
        Text(value, color = SxColors.Ink, fontSize = 12.5.sp, fontWeight = FontWeight.SemiBold)
    }
}

@Composable
private fun HealthGrid(snapshot: DeviceSnapshot) {
    Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
        Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            HealthCard("Power", snapshot.powerStatus, Modifier.weight(1f))
            HealthCard("Temperature", snapshot.temperatureStatus, Modifier.weight(1f))
        }
        Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            HealthCard("Firmware", snapshot.firmwareVersion, Modifier.weight(1f))
            HealthCard("Signal", snapshot.signalStrength, Modifier.weight(1f))
        }
    }
}

@Composable
private fun HealthCard(label: String, value: String, modifier: Modifier = Modifier) {
    SxCard(modifier = modifier) {
        Column {
            Text(
                label.uppercase(),
                color = SxColors.Faint,
                fontSize = 10.sp,
                fontWeight = FontWeight.Bold,
                style = TextStyle(letterSpacing = 0.4.sp),
            )
            Text(
                value,
                color = SxColors.Ink,
                fontSize = 14.sp,
                fontWeight = FontWeight.Bold,
                modifier = Modifier.padding(top = 4.dp),
            )
        }
    }
}

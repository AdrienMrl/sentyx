package com.sentyx.app.feature.pairing

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SentyxLogo
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxDimens
import com.sentyx.app.core.designsystem.SxEmptyState
import com.sentyx.app.core.designsystem.SxSecondaryButton
import com.sentyx.app.core.designsystem.SxSpinner
import com.sentyx.app.domain.model.DiscoveredDevice
import com.sentyx.app.domain.model.ScanState

/** BLE scan: spinner → device list or empty state. */
@Composable
fun ScanScreen(
    vm: PairingViewModel,
    onBack: () -> Unit,
    onDeviceSelected: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()
    LaunchedEffect(Unit) { vm.startScan() }

    OnboardingScreen {
        OnboardingBack(onBack)
        OnboardingTitle("Nearby devices", Modifier.padding(top = 14.dp))

        Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
            when {
                state.pairing -> ConnectingState(state.selectedDevice?.name)
                else -> when (val scan = state.scan) {
                    is ScanState.Scanning -> ScanningState()
                    is ScanState.Found -> FoundState(
                        devices = scan.devices,
                        error = state.pairingError,
                        onPick = { device -> vm.beginPairing(device, onDeviceSelected) },
                    )
                    is ScanState.NoneFound -> SxEmptyState(
                        emoji = "🔍",
                        title = "No devices found",
                        body = "Make sure the Pi is powered and within range. Bluetooth must be on.",
                    )
                }
            }
        }
        Spacer(Modifier.padding(top = 4.dp))
        SxSecondaryButton("Scan again", onClick = { vm.startScan() })
    }
}

@Composable
private fun ScanningState() {
    Column(
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(18.dp),
    ) {
        SxSpinner(size = 56.dp)
        Text("Scanning for Sentyx devices…", color = SxColors.Muted, fontSize = 14.sp, fontWeight = FontWeight.SemiBold)
    }
}

@Composable
private fun ConnectingState(deviceName: String?) {
    Column(
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(18.dp),
    ) {
        SxSpinner(size = 56.dp)
        Text(
            "Connecting to ${deviceName ?: "device"}…",
            color = SxColors.Muted,
            fontSize = 14.sp,
            fontWeight = FontWeight.SemiBold,
        )
    }
}

@Composable
private fun FoundState(
    devices: List<DiscoveredDevice>,
    error: String?,
    onPick: (DiscoveredDevice) -> Unit,
) {
    Column(
        modifier = Modifier.fillMaxWidth(),
        verticalArrangement = Arrangement.spacedBy(11.dp),
    ) {
        error?.let { message ->
            Text(
                message,
                color = SxColors.Red,
                fontSize = 12.5.sp,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier.fillMaxWidth().padding(bottom = 2.dp),
            )
        }
        devices.forEach { device ->
            DeviceRow(device, onClick = { onPick(device) })
        }
    }
}

@Composable
private fun DeviceRow(device: DiscoveredDevice, onClick: () -> Unit) {
    val shape = RoundedCornerShape(SxDimens.CardRadiusSmall)
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clip(shape)
            .background(SxColors.Card)
            .border(1.dp, SxColors.Border, shape)
            .clickable(onClick = onClick)
            .padding(14.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(13.dp),
    ) {
        SentyxLogo(size = 38.dp)
        Column(Modifier.weight(1f)) {
            Text(device.name, color = SxColors.Ink, fontSize = 14.sp, fontWeight = FontWeight.Bold)
            Text(device.subtitle, color = SxColors.Muted, fontSize = 12.sp, modifier = Modifier.padding(top = 2.dp))
        }
        Text("›", color = SxColors.Chevron, fontSize = 18.sp)
    }
}

package com.sentyx.app.feature.pairing

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
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
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxDimens
import com.sentyx.app.core.designsystem.SxPrimaryButton
import com.sentyx.app.domain.model.WifiNetwork

/** Pick a Wi-Fi network (with password) or skip to Bluetooth-only. */
@Composable
fun WifiSetupScreen(
    vm: PairingViewModel,
    onBack: () -> Unit,
    onContinue: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()
    LaunchedEffect(Unit) { vm.loadNetworks() }

    val selected = state.wifiNetworks.firstOrNull { it.ssid == state.selectedSsid }

    OnboardingScreen {
        OnboardingBack(onBack)
        OnboardingTitle("Connect to Wi-Fi", Modifier.padding(top = 14.dp))
        OnboardingSubtitle(
            "Wi-Fi lets Sentyx upload clips and reach the backend when you're away.",
            Modifier.padding(top = 10.dp),
        )
        // The network list is the screen's one scrollable region (weight makes it
        // absorb exactly the leftover height): a venue can broadcast a dozen APs,
        // and without this the Connect button was pushed clean off the screen.
        // The password field scrolls with the list — it belongs to the selected
        // row — while the buttons stay pinned below.
        Column(
            modifier = Modifier
                .weight(1f)
                .fillMaxWidth()
                .verticalScroll(rememberScrollState())
                .padding(top = 20.dp),
            verticalArrangement = Arrangement.spacedBy(9.dp),
        ) {
            state.wifiNetworks.forEach { net ->
                WifiRow(
                    net = net,
                    selected = net.ssid == state.selectedSsid,
                    onClick = { vm.selectNetwork(net.ssid) },
                )
            }
            if (selected != null && selected.requiresPassword) {
                PairEditField(
                    label = "Password for ${selected.ssid}",
                    value = state.wifiPassword,
                    onValueChange = vm::setWifiPassword,
                    masked = true,
                    modifier = Modifier.padding(top = 5.dp),
                )
            }
        }
        Spacer(Modifier.padding(top = 8.dp))
        Column(verticalArrangement = Arrangement.spacedBy(10.dp), horizontalAlignment = Alignment.CenterHorizontally) {
            // Disabled until a network is picked: connectWifi() treats a missing
            // selection as an invariant violation (it throws), so the gate is here.
            SxPrimaryButton(
                "Connect",
                enabled = state.selectedSsid != null,
                onClick = {
                    vm.connectWifi()
                    onContinue()
                },
            )
            Text(
                "Skip — use Bluetooth only",
                color = SxColors.Muted,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier
                    .clip(RoundedCornerShape(8.dp))
                    .clickable(onClick = onContinue)
                    .padding(4.dp),
            )
        }
    }
}

@Composable
private fun WifiRow(net: WifiNetwork, selected: Boolean, onClick: () -> Unit) {
    val shape = RoundedCornerShape(13.dp)
    val borderColor = if (selected) SxColors.Sand else SxColors.Border
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clip(shape)
            .background(SxColors.Card)
            .border(1.dp, borderColor, shape)
            .clickable(onClick = onClick)
            .padding(horizontal = 14.dp, vertical = 13.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Text("📶", fontSize = 15.sp)
        Column(Modifier.weight(1f)) {
            Text(net.ssid, color = SxColors.Ink, fontSize = 14.sp, fontWeight = FontWeight.Bold)
            Text(net.subtitle, color = SxColors.Muted, fontSize = 11.5.sp)
        }
        if (selected) {
            Text("✓", color = SxColors.Green, fontSize = 14.sp, fontWeight = FontWeight.Bold)
        }
    }
}

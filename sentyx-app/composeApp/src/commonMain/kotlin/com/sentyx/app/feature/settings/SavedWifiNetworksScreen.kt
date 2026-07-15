package com.sentyx.app.feature.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.PulsingDot
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxBottomSheet
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxDimens
import com.sentyx.app.core.designsystem.SxPrimaryButton
import com.sentyx.app.core.designsystem.SxSecondaryButton
import com.sentyx.app.core.designsystem.SxSectionHeader
import com.sentyx.app.core.designsystem.SxSpinner
import com.sentyx.app.domain.model.CurrentWifi
import com.sentyx.app.domain.model.SavedWifiNetwork
import com.sentyx.app.domain.model.WifiNetwork
import com.sentyx.app.feature.pairing.PairEditField

/**
 * Saved Wi-Fi networks, managed over BLE. Connects to the paired Pi, shows the
 * current + saved networks, and lets the user forget one (with a confirmation
 * warning when it's the active network) or add a new one (scan → pick →
 * password → connect). Mirrors the onboarding Wi-Fi step's components/tone.
 */
@Composable
fun SavedWifiNetworksScreen(
    vm: SavedWifiNetworksViewModel,
    onBack: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()

    Box(Modifier.fillMaxSize()) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(top = 56.dp)
                .padding(horizontal = 22.dp),
        ) {
            SxBackHeader(title = "Wi-Fi networks", onBack = onBack, modifier = Modifier.padding(top = 8.dp))

            when (state.phase) {
                WifiLinkPhase.Connecting -> CenteredBlock(Modifier.weight(1f)) {
                    SxSpinner(size = 56.dp)
                    Text(
                        "Connecting to your Sentyx Pi…",
                        color = SxColors.Muted,
                        fontSize = 14.sp,
                        fontWeight = FontWeight.SemiBold,
                    )
                }

                WifiLinkPhase.Failed -> CenteredBlock(Modifier.weight(1f)) {
                    Text("📶", fontSize = 38.sp)
                    Text(
                        "Couldn't connect",
                        color = SxColors.Ink,
                        fontSize = 16.sp,
                        fontWeight = FontWeight.ExtraBold,
                        textAlign = TextAlign.Center,
                    )
                    Text(
                        state.linkError ?: "Make sure the Pi is powered and Bluetooth is on.",
                        color = SxColors.Muted,
                        fontSize = 13.sp,
                        textAlign = TextAlign.Center,
                    )
                    Spacer(Modifier.height(4.dp))
                    SxSecondaryButton("Try again", onClick = vm::connect, modifier = Modifier.fillMaxWidth(0.7f))
                }

                WifiLinkPhase.Ready -> ReadyContent(Modifier.weight(1f), state, vm)
            }
        }

        state.forgetTarget?.let { target ->
            ForgetDialog(target = target, onConfirm = vm::confirmForget, onCancel = vm::cancelForget)
        }
        state.add?.let { add ->
            AddNetworkSheet(add = add, vm = vm)
        }
    }
}

@Composable
private fun CenteredBlock(modifier: Modifier = Modifier, content: @Composable () -> Unit) {
    Box(modifier.fillMaxWidth(), contentAlignment = Alignment.Center) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            content()
        }
    }
}

@Composable
private fun ReadyContent(modifier: Modifier, state: SavedWifiUiState, vm: SavedWifiNetworksViewModel) {
    Column(
        modifier = modifier
            .fillMaxWidth()
            .verticalScroll(rememberScrollState())
            .padding(bottom = 40.dp),
    ) {
        SxSectionHeader("Current network")
        CurrentNetworkCard(state.status?.current)

        state.statusError?.let { message ->
            InlineError(message = message, onRetry = vm::retryStatus)
        }

        SxSectionHeader("Saved networks")
        val saved = state.status?.saved.orEmpty()
        if (saved.isEmpty()) {
            EmptyCard("No saved networks yet. Add one below.")
        } else {
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(SxDimens.CardRadiusSmall))
                    .background(SxColors.Card)
                    .border(1.dp, SxColors.Border, RoundedCornerShape(SxDimens.CardRadiusSmall)),
            ) {
                saved.forEachIndexed { index, network ->
                    SavedNetworkRow(
                        network = network,
                        forgetting = state.forgettingSsid == network.ssid,
                        showDivider = index != saved.lastIndex,
                        onForget = { vm.requestForget(network) },
                    )
                }
            }
        }

        Spacer(Modifier.height(18.dp))
        SxPrimaryButton("Add a network", onClick = vm::openAdd)
    }
}

@Composable
private fun CurrentNetworkCard(current: CurrentWifi?) {
    val shape = RoundedCornerShape(SxDimens.CardRadiusSmall)
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clip(shape)
            .background(SxColors.Card)
            .border(1.dp, SxColors.Border, shape)
            .padding(horizontal = 14.dp, vertical = 14.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Text("📶", fontSize = 16.sp)
        Column(Modifier.weight(1f)) {
            if (current != null) {
                Text(current.ssid, color = SxColors.Ink, fontSize = 14.sp, fontWeight = FontWeight.Bold)
                Text(
                    "Connected · signal ${current.signal}%",
                    color = SxColors.Muted,
                    fontSize = 11.5.sp,
                    modifier = Modifier.padding(top = 1.dp),
                )
            } else {
                Text("Not connected", color = SxColors.Ink, fontSize = 14.sp, fontWeight = FontWeight.Bold)
                Text(
                    "The Pi isn't on Wi-Fi right now.",
                    color = SxColors.Muted,
                    fontSize = 11.5.sp,
                    modifier = Modifier.padding(top = 1.dp),
                )
            }
        }
        if (current != null) {
            PulsingDot(color = SxColors.Green, size = 9.dp)
        }
    }
}

@Composable
private fun SavedNetworkRow(
    network: SavedWifiNetwork,
    forgetting: Boolean,
    showDivider: Boolean,
    onForget: () -> Unit,
) {
    Column(Modifier.fillMaxWidth()) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = SxDimens.ListRowHPadding, vertical = SxDimens.ListRowVPadding),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            Column(Modifier.weight(1f)) {
                Text(network.ssid, color = SxColors.Ink, fontSize = 13.5.sp, fontWeight = FontWeight.SemiBold)
                val sub = when {
                    network.active -> "Active"
                    network.autoconnect -> "Auto-join"
                    else -> "Saved"
                }
                val subColor = if (network.active) SxColors.Green else SxColors.Muted
                Text(sub, color = subColor, fontSize = 11.5.sp, modifier = Modifier.padding(top = 1.dp))
            }
            if (forgetting) {
                SxSpinner(size = 18.dp, strokeWidth = 2.dp)
            } else {
                Text(
                    "Forget",
                    color = SxColors.Red,
                    fontSize = 13.sp,
                    fontWeight = FontWeight.Bold,
                    modifier = Modifier
                        .clip(RoundedCornerShape(8.dp))
                        .clickable(onClick = onForget)
                        .padding(horizontal = 6.dp, vertical = 4.dp),
                )
            }
        }
        if (showDivider) {
            Box(Modifier.fillMaxWidth().height(1.dp).background(SxColors.Divider))
        }
    }
}

@Composable
private fun EmptyCard(text: String) {
    val shape = RoundedCornerShape(SxDimens.CardRadiusSmall)
    Box(
        modifier = Modifier
            .fillMaxWidth()
            .clip(shape)
            .background(SxColors.Card)
            .border(1.dp, SxColors.Border, shape)
            .padding(horizontal = 14.dp, vertical = 16.dp),
    ) {
        Text(text, color = SxColors.Muted, fontSize = 13.sp)
    }
}

@Composable
private fun InlineError(message: String, onRetry: () -> Unit) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(top = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Text(message, color = SxColors.Red, fontSize = 12.5.sp, fontWeight = FontWeight.SemiBold, modifier = Modifier.weight(1f))
        Text(
            "Retry",
            color = SxColors.Bronze,
            fontSize = 12.5.sp,
            fontWeight = FontWeight.Bold,
            modifier = Modifier.clip(RoundedCornerShape(8.dp)).clickable(onClick = onRetry).padding(4.dp),
        )
    }
}

// ---- Forget confirmation ----------------------------------------------------

@Composable
private fun ForgetDialog(
    target: SavedWifiNetwork,
    onConfirm: () -> Unit,
    onCancel: () -> Unit,
) {
    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(SxColors.Ink.copy(alpha = 0.40f))
            .clickable(onClick = onCancel),
        contentAlignment = Alignment.Center,
    ) {
        Column(
            modifier = Modifier
                .padding(horizontal = 36.dp)
                .clip(RoundedCornerShape(18.dp))
                .background(SxColors.Bg)
                .clickable(enabled = false, onClick = {})
                .padding(20.dp),
            verticalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            Text("Forget \"${target.ssid}\"?", color = SxColors.Ink, fontSize = 16.sp, fontWeight = FontWeight.ExtraBold)
            val body = if (target.active) {
                "This is the network the Pi is using now. Forgetting it will drop its Wi-Fi connection until it joins another saved network."
            } else {
                "The Pi will no longer join this network automatically."
            }
            Text(body, color = SxColors.InkSecondary, fontSize = 13.sp, lineHeight = 19.sp)
            Row(
                modifier = Modifier.fillMaxWidth().padding(top = 6.dp),
                horizontalArrangement = Arrangement.spacedBy(10.dp),
            ) {
                SxSecondaryButton("Cancel", onClick = onCancel, modifier = Modifier.weight(1f))
                Box(
                    modifier = Modifier
                        .weight(1f)
                        .clip(RoundedCornerShape(SxDimens.ButtonRadius))
                        .background(SxColors.Red)
                        .clickable(onClick = onConfirm)
                        .padding(vertical = SxDimens.PrimaryButtonVPadding),
                    contentAlignment = Alignment.Center,
                ) {
                    Text("Forget", color = SxColors.White, fontSize = 15.sp, fontWeight = FontWeight.Bold)
                }
            }
        }
    }
}

// ---- Add-network sheet ------------------------------------------------------

@Composable
private fun AddNetworkSheet(add: AddNetworkState, vm: SavedWifiNetworksViewModel) {
    SxBottomSheet(onDismiss = vm::closeAdd) {
        Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Text("Add a network", color = SxColors.Ink, fontSize = 18.sp, fontWeight = FontWeight.ExtraBold)
            when {
                add.connectingSsid != null -> ConnectingBlock(add.connectingSsid)
                add.passwordFor != null -> PasswordBlock(add, vm)
                add.scanning -> ScanningBlock()
                add.scanError != null -> ScanErrorBlock(add.scanError, vm)
                else -> ScanResults(add, vm)
            }
        }
    }
}

@Composable
private fun ConnectingBlock(ssid: String) {
    Column(
        modifier = Modifier.fillMaxWidth().padding(vertical = 12.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        SxSpinner(size = 48.dp)
        Text("Joining \"$ssid\"…", color = SxColors.Ink, fontSize = 14.sp, fontWeight = FontWeight.Bold)
        Text(
            "The device's Wi-Fi will briefly disconnect while it joins. Bluetooth stays connected — this can take up to a minute.",
            color = SxColors.Muted,
            fontSize = 12.5.sp,
            textAlign = TextAlign.Center,
        )
    }
}

@Composable
private fun PasswordBlock(add: AddNetworkState, vm: SavedWifiNetworksViewModel) {
    val network = add.passwordFor ?: return
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        PairEditField(
            label = "Password for ${network.ssid}",
            value = add.password,
            onValueChange = vm::setPassword,
            masked = true,
        )
        add.connectError?.let {
            Text(it, color = SxColors.Red, fontSize = 12.5.sp, fontWeight = FontWeight.SemiBold)
        }
        SxPrimaryButton("Join", onClick = vm::submitPassword)
        Text(
            "Back to networks",
            color = SxColors.Muted,
            fontSize = 13.sp,
            fontWeight = FontWeight.SemiBold,
            textAlign = TextAlign.Center,
            modifier = Modifier
                .fillMaxWidth()
                .clip(RoundedCornerShape(8.dp))
                .clickable(onClick = vm::dismissPassword)
                .padding(4.dp),
        )
    }
}

@Composable
private fun ScanningBlock() {
    Column(
        modifier = Modifier.fillMaxWidth().padding(vertical = 20.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        SxSpinner(size = 44.dp)
        Text("Scanning for networks…", color = SxColors.Muted, fontSize = 13.5.sp, fontWeight = FontWeight.SemiBold)
    }
}

@Composable
private fun ScanErrorBlock(message: String, vm: SavedWifiNetworksViewModel) {
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Text(message, color = SxColors.Red, fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
        SxSecondaryButton("Scan again", onClick = vm::rescan)
    }
}

@Composable
private fun ScanResults(add: AddNetworkState, vm: SavedWifiNetworksViewModel) {
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(9.dp)) {
        add.connectError?.let {
            Text(it, color = SxColors.Red, fontSize = 12.5.sp, fontWeight = FontWeight.SemiBold)
        }
        if (add.networks.isEmpty()) {
            Text("No networks found nearby.", color = SxColors.Muted, fontSize = 13.sp)
        } else {
            add.networks.forEach { net ->
                ScanRow(net = net, onClick = { vm.pickNetwork(net) })
            }
        }
        Spacer(Modifier.height(2.dp))
        SxSecondaryButton("Scan again", onClick = vm::rescan)
    }
}

@Composable
private fun ScanRow(net: WifiNetwork, onClick: () -> Unit) {
    val shape = RoundedCornerShape(13.dp)
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clip(shape)
            .background(SxColors.Card)
            .border(1.dp, SxColors.Border, shape)
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
        if (net.requiresPassword) {
            Text("🔒", fontSize = 13.sp)
        }
        Text("›", color = SxColors.Chevron, fontSize = 16.sp)
    }
}

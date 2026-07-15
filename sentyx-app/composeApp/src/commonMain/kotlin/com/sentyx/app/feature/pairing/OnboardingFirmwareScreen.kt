package com.sentyx.app.feature.pairing

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxPrimaryButton
import com.sentyx.app.core.designsystem.monoFamily

/** Required firmware update before finishing onboarding. */
@Composable
fun OnboardingFirmwareScreen(
    vm: PairingViewModel,
    onContinue: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()
    LaunchedEffect(Unit) { vm.loadFirmware() }
    val fw = state.firmware

    OnboardingScreen {
        OnboardingTitle("Firmware")
        Column(
            modifier = Modifier.weight(1f).fillMaxWidth(),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.Center,
        ) {
            Box(
                modifier = Modifier
                    .size(56.dp)
                    .clip(RoundedCornerShape(16.dp))
                    .background(SxColors.Card)
                    .border(1.dp, SxColors.Border, RoundedCornerShape(16.dp)),
                contentAlignment = Alignment.Center,
            ) {
                Text("⬆", fontSize = 24.sp)
            }
            Text(
                "Update available",
                color = SxColors.Ink,
                fontSize = 20.sp,
                fontWeight = FontWeight.ExtraBold,
                modifier = Modifier.padding(top = 16.dp),
            )
            if (fw != null) {
                val body = buildAnnotatedString {
                    append("${state.deviceName} is on ")
                    withStyle(SpanBold) { append(fw.currentVersion) }
                    append(". Version ")
                    withStyle(SpanBold) { append(fw.newVersion) }
                    append(" improves event detection and is required to continue.")
                }
                Text(
                    body,
                    color = SxColors.InkSecondary,
                    fontSize = 13.5.sp,
                    lineHeight = 21.sp,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.padding(top = 16.dp).widthIn(max = 280.dp),
                )
                Box(
                    modifier = Modifier
                        .padding(top = 16.dp)
                        .clip(RoundedCornerShape(12.dp))
                        .background(SxColors.Card)
                        .border(1.dp, SxColors.Border, RoundedCornerShape(12.dp))
                        .padding(horizontal = 14.dp, vertical = 11.dp),
                ) {
                    Text(fw.detailLine, color = SxColors.Muted, fontSize = 12.sp, fontFamily = monoFamily)
                }
            }
        }
        SxPrimaryButton("Update & finish", onContinue)
    }
}

private val SpanBold = TextStyle(color = SxColors.Ink, fontWeight = FontWeight.Bold).toSpanStyle()

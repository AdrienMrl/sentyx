package com.sentyx.app.feature.device

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxEmptyState
import com.sentyx.app.core.designsystem.SxPrimaryButton
import com.sentyx.app.core.designsystem.SxSpinner
import com.sentyx.app.core.designsystem.monoFamily

/** Firmware update offer + install action, with inline install progress. */
@Composable
fun FirmwareFlowScreen(
    vm: DeviceViewModel,
    onBack: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()
    val update = state.firmwareUpdate

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(top = 56.dp, bottom = 40.dp)
            .padding(horizontal = 22.dp),
    ) {
        SxBackHeader(title = "Firmware update", onBack = onBack, modifier = Modifier.padding(top = 8.dp))

        if (update == null) {
            Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
                SxEmptyState(
                    emoji = "✅",
                    title = "You're up to date",
                    body = "No firmware update is available right now.",
                )
            }
            return@Column
        }

        Column(
            modifier = Modifier.weight(1f).fillMaxWidth(),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp, Alignment.CenterVertically),
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
                "${update.currentVersion} → ${update.newVersion}",
                color = SxColors.Ink,
                fontSize = 20.sp,
                fontWeight = FontWeight.ExtraBold,
            )
            Text(
                update.summary,
                color = SxColors.InkSecondary,
                fontSize = 13.5.sp,
                textAlign = TextAlign.Center,
                modifier = Modifier.widthIn(max = 290.dp),
            )
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(12.dp))
                    .background(SxColors.Card)
                    .border(1.dp, SxColors.Border, RoundedCornerShape(12.dp))
                    .padding(horizontal = 14.dp, vertical = 11.dp),
            ) {
                Text(
                    "Steps: check → download → install → restart → verify",
                    color = SxColors.Muted,
                    fontSize = 12.sp,
                    fontFamily = monoFamily,
                )
            }
        }

        if (state.firmwareInstalling) {
            Box(Modifier.fillMaxWidth().padding(vertical = 8.dp), contentAlignment = Alignment.Center) {
                SxSpinner()
            }
        } else {
            SxPrimaryButton(text = "Install update", onClick = { vm.installFirmware(onComplete = onBack) })
        }
    }
}

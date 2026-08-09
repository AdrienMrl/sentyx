package com.sentyx.app.feature.device

import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
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
import androidx.compose.ui.graphics.Color
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
import com.sentyx.app.core.designsystem.StatusPill
import com.sentyx.app.core.designsystem.monoFamily
import com.sentyx.app.domain.model.FirmwareStage
import com.sentyx.app.domain.model.FirmwareUpdate

/**
 * Firmware update offer, install action, and live install progress.
 *
 * The install is not something the app performs: tapping the button asks the
 * backend to deliver the release, and the device downloads, verifies and
 * installs it on its own — deliberately waiting for a moment when the car isn't
 * recording. That wait is a normal state, not a stall, so it gets an explicit
 * explanation rather than an indeterminate spinner. The screen stays open
 * throughout and reflects whatever the device last reported.
 */
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
            StageBadge(update.stage)
            Text(
                "${update.currentVersion} → ${update.newVersion}",
                color = SxColors.Ink,
                fontSize = 20.sp,
                fontWeight = FontWeight.ExtraBold,
            )
            val tone = stageTone(update.stage)
            StatusPill(
                text = update.stage.label,
                color = tone.fg,
                bg = tone.bg,
                pulsing = update.inProgress,
            )
            Text(
                update.summary,
                color = SxColors.InkSecondary,
                fontSize = 13.5.sp,
                textAlign = TextAlign.Center,
                modifier = Modifier.widthIn(max = 290.dp),
            )
            if (update.progressPct in 1..99) {
                ProgressBar(percent = update.progressPct)
            }
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(12.dp))
                    .background(SxColors.Card)
                    .border(1.dp, SxColors.Border, RoundedCornerShape(12.dp))
                    .padding(horizontal = 14.dp, vertical = 11.dp),
            ) {
                Text(
                    update.detailLine,
                    color = if (update.errorMessage != null) SxColors.RedDeep else SxColors.Muted,
                    fontSize = 12.sp,
                    fontFamily = monoFamily,
                )
            }
        }

        when {
            // The request round-trip only; the install itself is the device's job.
            state.firmwareInstalling -> Box(
                Modifier.fillMaxWidth().padding(vertical = 8.dp),
                contentAlignment = Alignment.Center,
            ) { SxSpinner() }

            update.actionable -> SxPrimaryButton(
                text = if (update.stage == FirmwareStage.Available) "Install update" else "Try again",
                onClick = vm::installFirmware,
            )

            // In-flight: nothing to press. Say what happens next instead of
            // offering a button that would only open a duplicate request.
            else -> Text(
                "Your device is handling this. You can close the app — it continues on its own.",
                color = SxColors.Muted,
                fontSize = 12.5.sp,
                textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth().padding(vertical = 12.dp),
            )
        }
    }
}

@Composable
private fun StageBadge(stage: FirmwareStage) {
    val glyph = when (stage) {
        FirmwareStage.Failed, FirmwareStage.RolledBack -> "⚠"
        FirmwareStage.WaitingSafe -> "🅿"
        else -> "⬆"
    }
    Box(
        modifier = Modifier
            .size(56.dp)
            .clip(RoundedCornerShape(16.dp))
            .background(SxColors.Card)
            .border(1.dp, SxColors.Border, RoundedCornerShape(16.dp)),
        contentAlignment = Alignment.Center,
    ) {
        Text(glyph, fontSize = 24.sp)
    }
}

/** Determinate progress, shown only when the device reports a real percentage. */
@Composable
private fun ProgressBar(percent: Int) {
    val fraction by animateFloatAsState(percent.coerceIn(0, 100) / 100f)
    Box(
        Modifier
            .fillMaxWidth()
            .height(6.dp)
            .clip(RoundedCornerShape(999.dp))
            .background(SxColors.Border),
    ) {
        Box(
            Modifier
                .fillMaxWidth(fraction)
                .fillMaxHeight()
                .clip(RoundedCornerShape(999.dp))
                .background(SxColors.Bronze),
        )
    }
}

private data class StageTone(val fg: Color, val bg: Color)

private fun stageTone(stage: FirmwareStage): StageTone = when (stage) {
    FirmwareStage.Failed, FirmwareStage.RolledBack -> StageTone(SxColors.RedDeep, SxColors.UrgentBg)
    FirmwareStage.WaitingSafe -> StageTone(SxColors.AmberDeep, SxColors.AttentionBg)
    else -> StageTone(SxColors.Bronze, SxColors.RoutineBg)
}

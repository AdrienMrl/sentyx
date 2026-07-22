package com.sentyx.app.feature.events

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
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
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.feature.player.VideoPlayer
import com.sentyx.app.core.designsystem.StripedThumb
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxPrimaryButton
import com.sentyx.app.core.designsystem.SxSpinner
import com.sentyx.app.core.designsystem.monoFamily
import com.sentyx.app.core.designsystem.severityColors
import com.sentyx.app.core.designsystem.severityLabel
import com.sentyx.app.domain.model.AnalysisFeedback
import com.sentyx.app.domain.model.AnalysisState
import com.sentyx.app.domain.model.EventWithMeta
import com.sentyx.app.domain.model.Moment
import com.sentyx.app.domain.model.Severity
import com.sentyx.app.domain.model.TransferRoute

/**
 * Event detail. Edge-to-edge video area over the analysis breakdown (Gemini
 * summary, observed facts vs. AI read, meaningful moments, feedback), retention,
 * action row and a download flow with a route bottom sheet. Ports the design
 * prototype's `dt` assembly. Navigation is exclusively via callbacks.
 */
@Composable
fun EventDetailScreen(
    vm: EventDetailViewModel,
    onBack: () -> Unit,
    onGoToTransfers: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()
    val item = state.event

    Box(Modifier.fillMaxSize().background(SxColors.Bg)) {
        if (item == null) {
            SxSpinner(Modifier.align(Alignment.Center))
        } else {
            DetailContent(state = state, item = item, vm = vm, onBack = onBack, onGoToTransfers = onGoToTransfers)

            if (state.severitySheet) {
                SeveritySheet(
                    current = item.severity,
                    onPick = vm::pickSeverity,
                    onDismiss = vm::dismissSeveritySheet,
                )
            }
            if (state.routeSheet) {
                RouteSheet(
                    sizeLabel = item.event.originalSizeLabel,
                    routes = vm.availableRoutes(),
                    selected = state.selectedRoute,
                    onSelect = vm::selectRoute,
                    onStart = { vm.startDownload(onView = onGoToTransfers); onGoToTransfers() },
                    onDismiss = vm::dismissRouteSheet,
                )
            }
            state.clip?.let { clip ->
                if (state.fullscreen) {
                    FullScreenPlayer(clip = clip, onClose = vm::closeFullscreen)
                }
            }
        }
    }
}

/**
 * Full-screen playback overlay: the same [VideoPlayer] filling a black-backed
 * [Dialog] (a true dialog window on Android, a full-bleed overlay on iOS), with
 * a close affordance. Kept intentionally minimal — the native transport controls
 * come from the player itself.
 */
@Composable
private fun FullScreenPlayer(clip: com.sentyx.app.data.clip.ClipSource, onClose: () -> Unit) {
    Dialog(
        onDismissRequest = onClose,
        properties = DialogProperties(usePlatformDefaultWidth = false),
    ) {
        Box(Modifier.fillMaxSize().background(Color.Black)) {
            VideoPlayer(source = clip, modifier = Modifier.fillMaxSize(), autoPlay = true)
            Box(
                Modifier
                    .align(Alignment.TopEnd)
                    .padding(top = 44.dp, end = 16.dp)
                    .size(36.dp)
                    .clip(CircleShape)
                    .background(SxColors.Bg.copy(alpha = 0.9f))
                    .clickable(onClick = onClose),
                contentAlignment = Alignment.Center,
            ) {
                Text("✕", color = SxColors.Ink, fontSize = 16.sp)
            }
        }
    }
}

@Composable
private fun DetailContent(
    state: EventDetailUiState,
    item: EventWithMeta,
    vm: EventDetailViewModel,
    onBack: () -> Unit,
    onGoToTransfers: () -> Unit,
) {
    val event = item.event
    val cameras = event.cameras
    val activeCam = cameras.getOrNull(state.camIndex) ?: cameras.firstOrNull() ?: "—"
    val done = event.state == AnalysisState.Complete

    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(bottom = 30.dp)) {
        VideoArea(
            state = event.state,
            activeCam = activeCam,
            duration = event.durationLabel,
            seekPct = state.seekPct,
            clip = state.clip,
            // Unmount the inline player while the full-screen overlay is up so
            // two players never stream (and play audio) at once.
            playingInline = state.playingInline && !state.fullscreen,
            onPlay = vm::play,
            onFullscreen = vm::openFullscreen,
            onBack = onBack,
        )

        if (cameras.size > 1) {
            Row(
                Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(start = 20.dp, end = 20.dp, top = 12.dp),
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                cameras.forEachIndexed { index, cam ->
                    CameraChip(name = cam, selected = index == state.camIndex, onClick = { vm.selectCamera(index) })
                }
            }
        }

        // Severity badge + time·location + title.
        Column(Modifier.fillMaxWidth().padding(start = 20.dp, end = 20.dp, top = 16.dp), verticalArrangement = Arrangement.spacedBy(9.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(9.dp)) {
                item.severity?.let { sev ->
                    val c = severityColors(sev)
                    Box(
                        Modifier
                            .clip(RoundedCornerShape(999.dp))
                            .background(c.bg)
                            .clickable(onClick = vm::showSeveritySheet)
                            .padding(horizontal = 11.dp, vertical = 4.dp),
                    ) {
                        Text(
                            "${severityLabel(sev)} ⌄",
                            color = c.fg,
                            fontSize = 11.sp,
                            fontWeight = FontWeight.Bold,
                            style = TextStyle(letterSpacing = 0.4.sp),
                        )
                    }
                }
                Text("${event.time} · ${event.location}", color = SxColors.Muted, fontSize = 12.sp, fontWeight = FontWeight.SemiBold)
            }
            Text(
                event.title,
                color = SxColors.Ink,
                fontSize = 22.sp,
                fontWeight = FontWeight.ExtraBold,
                style = TextStyle(letterSpacing = (-0.3).sp),
            )
        }

        MetaGrid(
            vehicle = state.device?.name ?: "—",
            camera = activeCam,
            storage = if (done) "Car + cloud" else "Car only",
            available = if (done) "Original + preview" else "Pending",
        )

        if (done) {
            GeminiCard(event.confidencePct, event.description, event.subjects, event.contactDetected)
            FactsAndInterpretation(event.facts, event.interpretation)
            MomentsSection(event.moments, state.seekMoment, vm::jumpToMoment)
            FeedbackCard(current = item.meta.feedback, onPick = vm::submitFeedback)
        }

        when (event.state) {
            AnalysisState.Analyzing, AnalysisState.Uploading, AnalysisState.Writing, AnalysisState.Waiting ->
                AnalyzingCard(event.state)
            AnalysisState.Failed -> FailedCard(onRetry = vm::retry)
            else -> Unit
        }

        RetentionCard(
            onCar = event.retentionOnCar ?: "—",
            cloud = event.retentionCloud ?: "—",
            onExtend = vm::extendRetention,
        )

        ActionRow(
            reviewed = item.meta.reviewed,
            favorite = item.meta.favorite,
            onReview = vm::toggleReviewed,
            onFavorite = vm::toggleFavorite,
            onShare = vm::share,
            onDelete = vm::requestDelete,
        )

        Column(Modifier.fillMaxWidth().padding(start = 20.dp, end = 20.dp, top = 16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            SxPrimaryButton(
                text = "Download original · ${event.originalSizeLabel}",
                onClick = vm::showRouteSheet,
            )
            Text(
                "Wi-Fi recommended for the full-quality clip",
                color = SxColors.Muted,
                fontSize = 11.5.sp,
                modifier = Modifier.align(Alignment.CenterHorizontally),
            )
        }
    }
}

@Composable
private fun VideoArea(
    state: AnalysisState,
    activeCam: String,
    duration: String,
    seekPct: Int,
    clip: com.sentyx.app.data.clip.ClipSource?,
    playingInline: Boolean,
    onPlay: () -> Unit,
    onFullscreen: () -> Unit,
    onBack: () -> Unit,
) {
    val playable = state == AnalysisState.Complete
    val notPlayable = state != AnalysisState.Complete && state != AnalysisState.Analyzing && state != AnalysisState.Failed
    Box(Modifier.fillMaxWidth().aspectRatio(16f / 11f)) {
        if (playingInline && clip != null) {
            // Real inline playback once the user taps play: the platform player
            // replaces the striped placeholder, with a full-screen chip on top.
            VideoPlayer(source = clip, modifier = Modifier.fillMaxSize(), autoPlay = true)
            Box(
                Modifier
                    .align(Alignment.TopEnd)
                    .padding(top = 60.dp, end = 16.dp)
                    .clip(RoundedCornerShape(8.dp))
                    .background(Color(0x8C2B271F))
                    .clickable(onClick = onFullscreen)
                    .padding(horizontal = 9.dp, vertical = 5.dp),
            ) {
                Text("⛶ Full screen", color = SxColors.OnInk, fontSize = 11.sp, fontWeight = FontWeight.Bold)
            }
        } else {
        StripedThumb(Modifier.fillMaxSize())

        if (playable) {
            Box(
                Modifier
                    .align(Alignment.Center)
                    .size(56.dp)
                    .clip(CircleShape)
                    .background(SxColors.Ink)
                    .clickable(onClick = onPlay),
                contentAlignment = Alignment.Center,
            ) {
                Text("▶", color = SxColors.OnInk, fontSize = 18.sp)
            }
            // Full-screen chip.
            Box(
                Modifier
                    .align(Alignment.TopEnd)
                    .padding(top = 60.dp, end = 16.dp)
                    .clip(RoundedCornerShape(8.dp))
                    .background(Color(0x8C2B271F))
                    .clickable(onClick = onFullscreen)
                    .padding(horizontal = 9.dp, vertical = 5.dp),
            ) {
                Text("⛶ Full screen", color = SxColors.OnInk, fontSize = 11.sp, fontWeight = FontWeight.Bold)
            }
            // Camera · duration chip.
            Box(
                Modifier
                    .align(Alignment.BottomStart)
                    .padding(start = 16.dp, bottom = 44.dp)
                    .clip(RoundedCornerShape(6.dp))
                    .background(Color(0x992B271F))
                    .padding(horizontal = 9.dp, vertical = 4.dp),
            ) {
                Text("$activeCam · $duration", color = SxColors.OnInk, fontSize = 10.5.sp, fontWeight = FontWeight.SemiBold)
            }
            // Seek bar.
            Box(
                Modifier
                    .align(Alignment.BottomCenter)
                    .fillMaxWidth()
                    .padding(start = 16.dp, end = 16.dp, bottom = 14.dp)
                    .height(4.dp)
                    .clip(RoundedCornerShape(2.dp))
                    .background(Color(0x402B271F)),
            ) {
                Box(
                    Modifier
                        .fillMaxWidth(seekPct / 100f)
                        .height(4.dp)
                        .clip(RoundedCornerShape(2.dp))
                        .background(SxColors.Bg),
                )
            }
        } else if (notPlayable) {
            Column(
                Modifier.align(Alignment.Center),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.spacedBy(10.dp),
            ) {
                Text(analysisStateIcon(state), fontSize = 30.sp)
                Text(
                    analysisPillStyle(state)?.label ?: "",
                    color = AiTextColor,
                    fontSize = 13.sp,
                    fontWeight = FontWeight.Bold,
                )
            }
        }
        }

        // Floating back button (top-left, ~60dp down).
        Box(
            Modifier
                .align(Alignment.TopStart)
                .padding(top = 60.dp, start = 16.dp)
                .size(36.dp)
                .clip(CircleShape)
                .background(SxColors.Bg.copy(alpha = 0.9f))
                .clickable(onClick = onBack),
            contentAlignment = Alignment.Center,
        ) {
            Text("‹", color = SxColors.Ink, fontSize = 18.sp)
        }
    }
}

@Composable
private fun CameraChip(name: String, selected: Boolean, onClick: () -> Unit) {
    val bg = if (selected) SxColors.Ink else SxColors.Card
    val fg = if (selected) SxColors.OnInk else SxColors.InkSecondary
    val border = if (selected) SxColors.Ink else SxColors.Border
    Box(
        Modifier
            .clip(RoundedCornerShape(10.dp))
            .background(bg)
            .border(1.dp, border, RoundedCornerShape(10.dp))
            .clickable(onClick = onClick)
            .padding(horizontal = 13.dp, vertical = 7.dp),
    ) {
        Text(name, color = fg, fontSize = 12.sp, fontWeight = FontWeight.Bold)
    }
}

@Composable
private fun MetaGrid(vehicle: String, camera: String, storage: String, available: String) {
    val shape = RoundedCornerShape(14.dp)
    Column(
        Modifier
            .fillMaxWidth()
            .padding(start = 20.dp, end = 20.dp, top = 14.dp)
            .clip(shape)
            .background(SxColors.Border)
            .border(1.dp, SxColors.Border, shape),
        verticalArrangement = Arrangement.spacedBy(1.dp),
    ) {
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(1.dp)) {
            MetaCell("Vehicle", vehicle, Modifier.weight(1f))
            MetaCell("Camera", camera, Modifier.weight(1f))
        }
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(1.dp)) {
            MetaCell("Storage", storage, Modifier.weight(1f))
            MetaCell("Available", available, Modifier.weight(1f))
        }
    }
}

@Composable
private fun MetaCell(label: String, value: String, modifier: Modifier) {
    Column(modifier.background(SxColors.Card).padding(horizontal = 13.dp, vertical = 11.dp)) {
        Text(
            label.uppercase(),
            color = SxColors.Faint,
            fontSize = 9.5.sp,
            fontWeight = FontWeight.Bold,
            style = TextStyle(letterSpacing = 0.6.sp),
        )
        Text(value, color = SxColors.Ink, fontSize = 12.5.sp, fontWeight = FontWeight.Bold, modifier = Modifier.padding(top = 3.dp))
    }
}

@Composable
private fun GeminiCard(confidencePct: Int?, description: String?, subjects: List<String>, contact: Boolean) {
    Column(
        Modifier
            .fillMaxWidth()
            .padding(start = 20.dp, end = 20.dp, top = 16.dp)
            .clip(RoundedCornerShape(16.dp))
            .background(SxColors.Card)
            .border(1.dp, SxColors.Border, RoundedCornerShape(16.dp))
            .padding(15.dp),
        verticalArrangement = Arrangement.spacedBy(11.dp),
    ) {
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
            Text("Gemini summary", color = SxColors.Bronze, fontSize = 12.sp, fontWeight = FontWeight.Bold)
            if (confidencePct != null) {
                Text("$confidencePct% confident", color = SxColors.Bronze, fontSize = 11.sp, fontWeight = FontWeight.Bold)
            }
        }
        if (description != null) {
            Text(description, color = AiTextColor, fontSize = 13.5.sp)
        }
        Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(7.dp)) {
            subjects.forEach { subject ->
                Box(
                    Modifier.clip(RoundedCornerShape(8.dp)).background(SxColors.Divider).padding(horizontal = 9.dp, vertical = 4.dp),
                ) {
                    Text(subject, color = SxColors.InkSecondary, fontSize = 11.sp, fontWeight = FontWeight.SemiBold)
                }
            }
            val contactBg = if (contact) SxColors.UrgentBg else SxColors.Green.copy(alpha = 0.12f)
            val contactFg = if (contact) SxColors.RedDeep else SxColors.Green
            Box(Modifier.clip(RoundedCornerShape(8.dp)).background(contactBg).padding(horizontal = 9.dp, vertical = 4.dp)) {
                Text(
                    if (contact) "Contact detected" else "No contact",
                    color = contactFg,
                    fontSize = 11.sp,
                    fontWeight = FontWeight.Bold,
                )
            }
        }
    }
}

@Composable
private fun FactsAndInterpretation(facts: List<String>, interpretation: String?) {
    Column(Modifier.fillMaxWidth().padding(start = 20.dp, end = 20.dp, top = 12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        SectionLabel("Observed facts")
        Column(
            Modifier
                .fillMaxWidth()
                .clip(RoundedCornerShape(14.dp))
                .background(SxColors.Card)
                .border(1.dp, SxColors.Border, RoundedCornerShape(14.dp))
                .padding(horizontal = 14.dp),
        ) {
            facts.forEachIndexed { index, fact ->
                Row(
                    Modifier.fillMaxWidth().padding(vertical = 8.dp),
                    horizontalArrangement = Arrangement.spacedBy(9.dp),
                ) {
                    Text("✓", color = SxColors.Green, fontSize = 12.sp)
                    Text(fact, color = AiTextColor, fontSize = 13.sp)
                }
                if (index < facts.lastIndex) {
                    Box(Modifier.fillMaxWidth().height(1.dp).background(SxColors.Divider))
                }
            }
        }
        if (interpretation != null) {
            Row(
                Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(14.dp))
                    .background(SxColors.Bronze.copy(alpha = 0.09f))
                    .border(1.dp, SxColors.Bronze.copy(alpha = 0.2f), RoundedCornerShape(14.dp))
                    .padding(horizontal = 14.dp, vertical = 12.dp),
                horizontalArrangement = Arrangement.spacedBy(9.dp),
            ) {
                Text(
                    "AI READ",
                    color = SxColors.Bronze,
                    fontSize = 11.sp,
                    fontWeight = FontWeight.ExtraBold,
                    style = TextStyle(letterSpacing = 0.5.sp),
                )
                Text(interpretation, color = SxColors.InkSecondary, fontSize = 12.5.sp)
            }
        }
    }
}

@Composable
private fun MomentsSection(moments: List<Moment>, activeMomentPct: Int?, onJump: (Int) -> Unit) {
    if (moments.isEmpty()) return
    Column(Modifier.fillMaxWidth()) {
        SectionLabel("Meaningful moments", Modifier.padding(start = 20.dp, end = 20.dp, top = 18.dp, bottom = 8.dp))
        Column(Modifier.fillMaxWidth().padding(horizontal = 20.dp)) {
            moments.forEach { moment ->
                val highlighted = activeMomentPct == moment.positionPct
                Row(
                    Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(11.dp))
                        .background(if (highlighted) MomentHighlight else Color.Transparent)
                        .clickable { onJump(moment.positionPct) }
                        .padding(horizontal = 12.dp, vertical = 11.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(14.dp),
                ) {
                    Text(moment.timestamp, color = SxColors.Bronze, fontSize = 12.sp, fontWeight = FontWeight.SemiBold, fontFamily = monoFamily, modifier = Modifier.size(width = 36.dp, height = 16.dp))
                    Text(moment.label, color = AiTextColor, fontSize = 13.5.sp, modifier = Modifier.weight(1f))
                    Text("▶ jump", color = SxColors.Chevron, fontSize = 11.sp)
                }
            }
        }
    }
}

@Composable
private fun FeedbackCard(current: AnalysisFeedback?, onPick: (AnalysisFeedback) -> Unit) {
    Column(
        Modifier
            .fillMaxWidth()
            .padding(start = 20.dp, end = 20.dp, top = 16.dp)
            .clip(RoundedCornerShape(14.dp))
            .background(SxColors.Card)
            .border(1.dp, SxColors.Border, RoundedCornerShape(14.dp))
            .padding(14.dp),
    ) {
        Text("Was this analysis right?", color = SxColors.Ink, fontSize = 12.5.sp, fontWeight = FontWeight.Bold, modifier = Modifier.padding(bottom = 10.dp))
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            FeedbackButton("👍 Accurate", current == AnalysisFeedback.Accurate, Modifier.weight(1f)) { onPick(AnalysisFeedback.Accurate) }
            FeedbackButton("👎 Off", current == AnalysisFeedback.Inaccurate, Modifier.weight(1f)) { onPick(AnalysisFeedback.Inaccurate) }
            FeedbackButton("⚑ False alarm", current == AnalysisFeedback.FalseAlarm, Modifier.weight(1f)) { onPick(AnalysisFeedback.FalseAlarm) }
        }
    }
}

@Composable
private fun FeedbackButton(label: String, selected: Boolean, modifier: Modifier, onClick: () -> Unit) {
    val bg = if (selected) SxColors.Ink else SxColors.Bg
    val fg = if (selected) SxColors.OnInk else SxColors.InkSecondary
    val border = if (selected) SxColors.Ink else SxColors.Border
    Box(
        modifier
            .clip(RoundedCornerShape(10.dp))
            .background(bg)
            .border(1.dp, border, RoundedCornerShape(10.dp))
            .clickable(onClick = onClick)
            .padding(vertical = 9.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(label, color = fg, fontSize = 12.sp, fontWeight = FontWeight.Bold)
    }
}

@Composable
private fun AnalyzingCard(state: AnalysisState) {
    val title = analysisPillStyle(state)?.label ?: "Analyzing"
    val body = when (state) {
        AnalysisState.Waiting -> "The Pi is offline. The clip will upload and analyze automatically once it reconnects."
        AnalysisState.Writing -> "The car is still writing this clip. Analysis begins as soon as it finishes."
        else -> "Gemini is analyzing this clip. This usually takes under a minute."
    }
    Column(
        Modifier
            .fillMaxWidth()
            .padding(start = 20.dp, end = 20.dp, top = 16.dp)
            .clip(RoundedCornerShape(16.dp))
            .background(SxColors.Card)
            .border(1.dp, SxColors.Border, RoundedCornerShape(16.dp))
            .padding(20.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        SxSpinner(size = 40.dp)
        Text(title, color = SxColors.Ink, fontSize = 14.sp, fontWeight = FontWeight.Bold)
        Text(body, color = SxColors.Muted, fontSize = 12.5.sp, textAlign = androidx.compose.ui.text.style.TextAlign.Center)
    }
}

@Composable
private fun FailedCard(onRetry: () -> Unit) {
    Column(
        Modifier
            .fillMaxWidth()
            .padding(start = 20.dp, end = 20.dp, top = 16.dp)
            .clip(RoundedCornerShape(16.dp))
            .background(SxColors.Card)
            .border(1.dp, UrgentCardBorder, RoundedCornerShape(16.dp))
            .padding(18.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Text("Analysis failed", color = SxColors.RedDeep, fontSize = 14.sp, fontWeight = FontWeight.Bold)
        Text(
            "Gemini couldn't process this clip. You can retry once the car re-uploads it.",
            color = SxColors.Muted,
            fontSize = 12.5.sp,
            textAlign = androidx.compose.ui.text.style.TextAlign.Center,
        )
        Box(
            Modifier
                .clip(RoundedCornerShape(11.dp))
                .background(SxColors.Ink)
                .clickable(onClick = onRetry)
                .padding(horizontal = 22.dp, vertical = 11.dp),
        ) {
            Text("Retry analysis", color = SxColors.OnInk, fontSize = 13.sp, fontWeight = FontWeight.Bold)
        }
    }
}

@Composable
private fun RetentionCard(onCar: String, cloud: String, onExtend: () -> Unit) {
    Column(
        Modifier
            .fillMaxWidth()
            .padding(start = 20.dp, end = 20.dp, top = 16.dp)
            .clip(RoundedCornerShape(14.dp))
            .background(SxColors.Card)
            .border(1.dp, SxColors.Border, RoundedCornerShape(14.dp))
            .padding(horizontal = 16.dp, vertical = 14.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Text(
            "RETENTION",
            color = SxColors.Faint,
            fontSize = 11.sp,
            fontWeight = FontWeight.Bold,
            style = TextStyle(letterSpacing = 0.5.sp),
        )
        RetentionRow("On car", onCar)
        RetentionRow("Cloud copy", cloud)
        Text(
            "Keep longer →",
            color = SxColors.Bronze,
            fontSize = 12.sp,
            fontWeight = FontWeight.Bold,
            modifier = Modifier.padding(top = 2.dp).clickable(onClick = onExtend),
        )
    }
}

@Composable
private fun RetentionRow(label: String, value: String) {
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
        Text(label, color = SxColors.Muted, fontSize = 12.5.sp)
        Text(value, color = SxColors.Ink, fontSize = 12.5.sp, fontWeight = FontWeight.SemiBold)
    }
}

@Composable
private fun ActionRow(
    reviewed: Boolean,
    favorite: Boolean,
    onReview: () -> Unit,
    onFavorite: () -> Unit,
    onShare: () -> Unit,
    onDelete: () -> Unit,
) {
    Row(
        Modifier.fillMaxWidth().padding(start = 20.dp, end = 20.dp, top = 14.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        ActionCell(if (reviewed) "✓" else "○", if (reviewed) "Reviewed" else "Review", if (reviewed) SxColors.Green else SxColors.Muted, Modifier.weight(1f), onReview)
        ActionCell(if (favorite) "★" else "☆", "Favorite", if (favorite) SxColors.Amber else SxColors.Muted, Modifier.weight(1f), onFavorite)
        ActionCell("↗", "Share", SxColors.Muted, Modifier.weight(1f), onShare)
        ActionCell("🗑", "Delete", SxColors.Red, Modifier.weight(1f), onDelete)
    }
}

@Composable
private fun ActionCell(icon: String, label: String, iconColor: Color, modifier: Modifier, onClick: () -> Unit) {
    Column(
        modifier
            .clip(RoundedCornerShape(12.dp))
            .background(SxColors.Card)
            .border(1.dp, SxColors.Border, RoundedCornerShape(12.dp))
            .clickable(onClick = onClick)
            .padding(horizontal = 4.dp, vertical = 11.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(5.dp),
    ) {
        Text(icon, color = iconColor, fontSize = 16.sp)
        Text(label, color = SxColors.InkSecondary, fontSize = 10.sp, fontWeight = FontWeight.Bold)
    }
}

@Composable
private fun SectionLabel(text: String, modifier: Modifier = Modifier) {
    Text(
        text.uppercase(),
        color = SxColors.Faint,
        fontSize = 10.5.sp,
        fontWeight = FontWeight.Bold,
        style = TextStyle(letterSpacing = 0.6.sp),
        modifier = modifier,
    )
}

// ---------------- Sheets ----------------

@Composable
private fun SeveritySheet(current: Severity?, onPick: (Severity) -> Unit, onDismiss: () -> Unit) {
    com.sentyx.app.core.designsystem.SxBottomSheet(onDismiss = onDismiss) {
        Text("Change severity", color = SxColors.Ink, fontSize = 16.sp, fontWeight = FontWeight.ExtraBold, modifier = Modifier.padding(bottom = 12.dp))
        Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            listOf(Severity.Urgent, Severity.Attention, Severity.Routine).forEach { sev ->
                val c = severityColors(sev)
                Row(
                    Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(13.dp))
                        .background(SxColors.Card)
                        .border(1.dp, SxColors.Border, RoundedCornerShape(13.dp))
                        .clickable { onPick(sev) }
                        .padding(horizontal = 14.dp, vertical = 13.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(11.dp),
                ) {
                    Box(Modifier.size(11.dp).clip(CircleShape).background(c.node))
                    Text(severityLabel(sev), color = SxColors.Ink, fontSize = 14.sp, fontWeight = FontWeight.Bold, modifier = Modifier.weight(1f))
                    if (current == sev) Text("✓", color = SxColors.Green, fontSize = 14.sp, fontWeight = FontWeight.Bold)
                }
            }
        }
    }
}

@Composable
private fun RouteSheet(
    sizeLabel: String,
    routes: List<Pair<TransferRoute, Boolean>>,
    selected: TransferRoute,
    onSelect: (TransferRoute) -> Unit,
    onStart: () -> Unit,
    onDismiss: () -> Unit,
) {
    com.sentyx.app.core.designsystem.SxBottomSheet(onDismiss = onDismiss) {
        Text("Download original", color = SxColors.Ink, fontSize = 16.sp, fontWeight = FontWeight.ExtraBold)
        Row(
            Modifier
                .fillMaxWidth()
                .padding(top = 12.dp)
                .clip(RoundedCornerShape(13.dp))
                .background(SxColors.Card)
                .border(1.dp, SxColors.Border, RoundedCornerShape(13.dp))
                .padding(horizontal = 14.dp, vertical = 12.dp),
            horizontalArrangement = Arrangement.SpaceBetween,
        ) {
            Text("$sizeLabel · ~1:14 clip", color = SxColors.Muted, fontSize = 12.5.sp)
            Text("41.6 GB phone free", color = SxColors.Ink, fontSize = 12.5.sp, fontWeight = FontWeight.Bold)
        }
        Text("Transfer route", color = SxColors.Muted, fontSize = 12.sp, fontWeight = FontWeight.Bold, modifier = Modifier.padding(top = 16.dp, bottom = 8.dp))
        Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            routes.forEach { (route, available) ->
                RouteOption(
                    route = route,
                    available = available,
                    selected = selected == route && available,
                    onClick = { if (available) onSelect(route) },
                )
            }
        }
        SxPrimaryButton(text = "Start download", onClick = onStart, modifier = Modifier.padding(top = 16.dp))
    }
}

@Composable
private fun RouteOption(route: TransferRoute, available: Boolean, selected: Boolean, onClick: () -> Unit) {
    val icon = when (route) {
        TransferRoute.WifiDirect -> "📶"
        TransferRoute.WifiLocal -> "🏠"
        TransferRoute.Bluetooth -> "ᔨ"
    }
    val label = when (route) {
        TransferRoute.WifiDirect -> "Direct Wi-Fi to car"
        TransferRoute.WifiLocal -> "Home Wi-Fi network"
        TransferRoute.Bluetooth -> "Bluetooth"
    }
    val sub = when (route) {
        TransferRoute.WifiDirect -> "Fastest · ~2 min"
        TransferRoute.WifiLocal -> "~3 min"
        TransferRoute.Bluetooth -> if (available) "Slow · ~40 min" else "Unavailable — Bluetooth off"
    }
    val border = if (selected) SxColors.Ink else SxColors.Border
    Row(
        Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(13.dp))
            .background(SxColors.Card)
            .border(1.dp, border, RoundedCornerShape(13.dp))
            .then(if (available) Modifier.clickable(onClick = onClick) else Modifier)
            .padding(horizontal = 14.dp, vertical = 13.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        val alpha = if (available) 1f else 0.5f
        Text(icon, fontSize = 17.sp, modifier = Modifier.alpha(alpha))
        Column(Modifier.weight(1f).alpha(alpha)) {
            Text(label, color = SxColors.Ink, fontSize = 13.5.sp, fontWeight = FontWeight.Bold)
            Text(sub, color = SxColors.Muted, fontSize = 11.5.sp)
        }
        if (selected) Text("✓", color = SxColors.Green, fontSize = 14.sp, fontWeight = FontWeight.Bold)
    }
}

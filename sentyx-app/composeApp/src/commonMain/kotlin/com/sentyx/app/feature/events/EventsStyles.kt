package com.sentyx.app.feature.events

import androidx.compose.ui.graphics.Color
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.domain.model.AnalysisState
import com.sentyx.app.domain.model.DeviceCondition

/**
 * Feature-local colors and status-style mappings ported from the design
 * prototype's `SEV` / `AN` / `DEVS` maps (design/Sentyx.dc.html). These are
 * specific to the events feature and complement the shared design system.
 */

/** Timeline connector line + node ring color. */
internal val TimelineConnector = Color(0xFFE0D8C8)

/** Body text used inside AI summary / facts (warmer than [SxColors.InkSecondary]). */
internal val AiTextColor = Color(0xFF4A4436)

/** Reddish border on urgent event cards. */
internal val UrgentCardBorder = Color(0xFFF0D9CE)

/** Background for the highlighted "meaningful moment" row. */
internal val MomentHighlight = Color(0xFFF0EADD)

/** Bronze-ish foreground used by in-flight pipeline pills. */
internal val PendingFg = Color(0xFF8A6D2E)

/** Softer red used for the bulk "Delete" action on the dark action bar. */
internal val BulkDeleteColor = Color(0xFFE39B8C)

/** Bronze translucent background for in-flight pipeline pills. */
internal val PendingBg = SxColors.Bronze.copy(alpha = 0.14f)

/** Style of the pipeline StatusPill for a given analysis state; null when complete. */
internal data class AnalysisPillStyle(
    val label: String,
    val color: Color,
    val bg: Color,
    val pulsing: Boolean,
)

/** Pill presentation for a pipeline state, or null when analysis is complete. */
internal fun analysisPillStyle(state: AnalysisState): AnalysisPillStyle? = when (state) {
    AnalysisState.Complete -> null
    AnalysisState.Writing -> AnalysisPillStyle("Recording on car", PendingFg, PendingBg, pulsing = true)
    AnalysisState.Uploading -> AnalysisPillStyle("Uploading clip", PendingFg, PendingBg, pulsing = true)
    AnalysisState.Analyzing -> AnalysisPillStyle("Gemini analyzing", PendingFg, PendingBg, pulsing = true)
    AnalysisState.Waiting -> AnalysisPillStyle("Waiting for upload · Pi offline", SxColors.Muted, SxColors.RoutineBg, pulsing = false)
    AnalysisState.Failed -> AnalysisPillStyle("Analysis failed · retry", SxColors.RedDeep, SxColors.UrgentBg, pulsing = false)
    AnalysisState.Encrypted -> AnalysisPillStyle("Encrypted clip", SxColors.Muted, SxColors.RoutineBg, pulsing = false)
    AnalysisState.Unsupported -> AnalysisPillStyle("Unsupported clip", SxColors.Muted, SxColors.RoutineBg, pulsing = false)
}

/** The video-area state glyph for a non-playable clip. */
internal fun analysisStateIcon(state: AnalysisState): String = when (state) {
    AnalysisState.Encrypted -> "🔒"
    AnalysisState.Unsupported -> "🚫"
    AnalysisState.Waiting -> "⏳"
    AnalysisState.Writing -> "⏺"
    else -> "⏳"
}

/** Color pair for the device status pill in the car tile. */
internal data class DeviceChipStyle(val color: Color, val bg: Color)

internal fun deviceChipStyle(condition: DeviceCondition): DeviceChipStyle = when (condition) {
    DeviceCondition.Online -> DeviceChipStyle(SxColors.Green, SxColors.Green.copy(alpha = 0.12f))
    DeviceCondition.Offline -> DeviceChipStyle(SxColors.Red, SxColors.Red.copy(alpha = 0.12f))
    DeviceCondition.NeedsAttention,
    DeviceCondition.UpdateRequired,
    DeviceCondition.LocalOnly,
    -> DeviceChipStyle(SxColors.AmberDeep, SxColors.Amber.copy(alpha = 0.14f))
}

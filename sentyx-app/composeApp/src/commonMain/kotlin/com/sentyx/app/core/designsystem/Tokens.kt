package com.sentyx.app.core.designsystem

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import com.sentyx.app.domain.model.Severity

/**
 * Named colors extracted from the Sentyx design prototype (warm cream palette).
 * This design is light-only; there is no dark variant.
 */
object SxColors {
    // Surfaces
    val Bg = Color(0xFFF4F0E9)
    val Card = Color(0xFFFBF8F2)
    val Border = Color(0xFFE7E0D2)
    val Divider = Color(0xFFEDE7DA)
    val Sand = Color(0xFFD8CEBB)

    // Text / ink
    val Ink = Color(0xFF2B271F)
    val InkSecondary = Color(0xFF6E6656)
    val Muted = Color(0xFF8A8271)
    val Faint = Color(0xFFA69E8B)
    val Hint = Color(0xFFB0A892)
    val Chevron = Color(0xFFC4BBA8)

    // Accents
    val Gold = Color(0xFFC9AE77)
    val Bronze = Color(0xFF9A7B3C)
    val Green = Color(0xFF2F7C4A)
    val Red = Color(0xFFB23A2E)
    val RedDeep = Color(0xFF9A2E22)
    val Amber = Color(0xFFC08A3E)
    val AmberDeep = Color(0xFF8A5A12)

    // Severity / status backgrounds
    val UrgentBg = Color(0xFFF3D9D2)
    val AttentionBg = Color(0xFFE9DCC4)
    val RoutineBg = Color(0xFFEAE4D8)

    // Hover states
    val HoverLight = Color(0xFFEDE7DA)
    val HoverDark = Color(0xFF463F31)

    // Severity node (timeline dot) colors
    val SeverityUrgentNode = Color(0xFFB23A2E)
    val SeverityAttentionNode = Color(0xFFC08A3E)
    val SeverityRoutineNode = Color(0xFFB8AD97)

    // Severity badge foreground (routine fg is warmer than the node)
    val SeverityUrgentFg = RedDeep
    val SeverityAttentionFg = AmberDeep
    val SeverityRoutineFg = Color(0xFF7A7060)

    // Common on-dark text color
    val OnInk = Bg
    val White = Color(0xFFFFFFFF)
}

/**
 * Layout dimensions from the design. Screen padding differs between main
 * screens (22.dp) and onboarding (26.dp).
 */
object SxDimens {
    val ScreenPadding = 22.dp
    val OnboardingPadding = 26.dp

    val CardRadius = 16.dp
    val CardRadiusSmall = 14.dp
    val ButtonRadius = 14.dp
    val ButtonRadiusSmall = 13.dp
    val InputRadius = 12.dp
    val ChipRadius = 999.dp
    val SheetRadius = 22.dp

    val PrimaryButtonVPadding = 16.dp
    val SecondaryButtonVPadding = 15.dp

    val ListRowVPadding = 14.dp
    val ListRowHPadding = 16.dp

    val ToggleWidth = 38.dp
    val ToggleHeight = 22.dp
    val ToggleKnob = 18.dp

    val DragHandleWidth = 38.dp
    val DragHandleHeight = 4.dp
}

/** Colors used to render a [Severity] badge (pill background + text). */
data class SeverityColors(val bg: Color, val fg: Color, val node: Color)

/** Background/foreground/node colors for a given severity. */
fun severityColors(severity: Severity): SeverityColors = when (severity) {
    Severity.Urgent -> SeverityColors(SxColors.UrgentBg, SxColors.SeverityUrgentFg, SxColors.SeverityUrgentNode)
    Severity.Attention -> SeverityColors(SxColors.AttentionBg, SxColors.SeverityAttentionFg, SxColors.SeverityAttentionNode)
    Severity.Routine -> SeverityColors(SxColors.RoutineBg, SxColors.SeverityRoutineFg, SxColors.SeverityRoutineNode)
}

/** Capitalized display label for a severity, e.g. "Urgent". */
fun severityLabel(severity: Severity): String = when (severity) {
    Severity.Urgent -> "Urgent"
    Severity.Attention -> "Attention"
    Severity.Routine -> "Routine"
}

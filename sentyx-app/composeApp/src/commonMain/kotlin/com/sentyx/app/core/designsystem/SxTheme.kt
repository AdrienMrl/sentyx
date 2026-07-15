package com.sentyx.app.core.designsystem

import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.text.font.FontFamily

/**
 * Monospace family used for the IBM Plex Mono roles in the design
 * (codes, timestamps, technical metadata). No custom font is bundled, so the
 * platform monospace face stands in.
 */
val monoFamily: FontFamily = FontFamily.Monospace

/**
 * Sentyx theme. Light-only, built from [SxColors]. DM Sans is approximated by
 * the platform sans-serif; IBM Plex Mono by [monoFamily].
 */
@Composable
fun SentyxTheme(content: @Composable () -> Unit) {
    val colors = lightColorScheme(
        primary = SxColors.Ink,
        onPrimary = SxColors.OnInk,
        secondary = SxColors.Bronze,
        onSecondary = SxColors.White,
        background = SxColors.Bg,
        onBackground = SxColors.Ink,
        surface = SxColors.Card,
        onSurface = SxColors.Ink,
        surfaceVariant = SxColors.HoverLight,
        onSurfaceVariant = SxColors.InkSecondary,
        outline = SxColors.Border,
        error = SxColors.Red,
        onError = SxColors.White,
    )
    MaterialTheme(
        colorScheme = colors,
        typography = Typography(),
        content = content,
    )
}

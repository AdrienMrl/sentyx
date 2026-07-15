package com.sentyx.app.core.designsystem

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/**
 * Uppercase, letter-spaced section label used above grouped lists. 12sp bold
 * faint ink, with 20dp top / 8dp bottom spacing baked in.
 */
@Composable
fun SxSectionHeader(
    text: String,
    modifier: Modifier = Modifier,
) {
    Text(
        text = text.uppercase(),
        color = SxColors.Faint,
        fontSize = 12.sp,
        fontWeight = FontWeight.Bold,
        style = TextStyle(letterSpacing = 0.5.sp),
        modifier = modifier.padding(top = 20.dp, bottom = 8.dp),
    )
}

/**
 * Sub-screen header: a "‹" back glyph button followed by a 22sp extrabold
 * title. Used by every detail/settings screen.
 */
@Composable
fun SxBackHeader(
    title: String,
    onBack: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Row(
        modifier = modifier,
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Text(
            "‹",
            color = SxColors.Muted,
            fontSize = 22.sp,
            modifier = Modifier
                .width(22.dp)
                .clickable(onClick = onBack),
        )
        Text(
            title,
            color = SxColors.Ink,
            fontSize = 22.sp,
            fontWeight = FontWeight.ExtraBold,
            style = TextStyle(letterSpacing = (-0.4).sp),
        )
    }
}

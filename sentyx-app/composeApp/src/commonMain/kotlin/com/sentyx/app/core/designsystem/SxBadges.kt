package com.sentyx.app.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.domain.model.Severity

/** Small severity pill using the severity's background + foreground colors. */
@Composable
fun SeverityBadge(
    severity: Severity,
    modifier: Modifier = Modifier,
) {
    val c = severityColors(severity)
    Box(
        modifier = modifier
            .clip(RoundedCornerShape(SxDimens.ChipRadius))
            .background(c.bg)
            .padding(horizontal = 9.dp, vertical = 3.dp),
    ) {
        Text(
            text = severityLabel(severity),
            color = c.fg,
            fontSize = 10.5.sp,
            fontWeight = FontWeight.Bold,
            style = TextStyle(letterSpacing = 0.4.sp),
        )
    }
}

/** Small colored dot for the given severity (timeline / list marker). */
@Composable
fun SeverityDot(
    severity: Severity,
    modifier: Modifier = Modifier,
    size: androidx.compose.ui.unit.Dp = 11.dp,
) {
    Box(
        modifier
            .size(size)
            .clip(CircleShape)
            .background(severityColors(severity).node),
    )
}

/**
 * Rounded status pill: a small dot (optionally [pulsing]) plus a bold 11sp
 * label, both in [color], over a [bg] background.
 */
@Composable
fun StatusPill(
    text: String,
    color: Color,
    bg: Color,
    modifier: Modifier = Modifier,
    pulsing: Boolean = false,
) {
    Row(
        modifier = modifier
            .clip(RoundedCornerShape(SxDimens.ChipRadius))
            .background(bg)
            .padding(horizontal = 10.dp, vertical = 5.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(7.dp),
    ) {
        if (pulsing) {
            PulsingDot(color = color, size = 6.dp)
        } else {
            Box(Modifier.size(6.dp).clip(CircleShape).background(color))
        }
        Text(text, color = color, fontSize = 11.sp, fontWeight = FontWeight.Bold)
    }
}

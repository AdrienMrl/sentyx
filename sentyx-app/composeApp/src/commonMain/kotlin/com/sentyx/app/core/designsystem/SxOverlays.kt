package com.sentyx.app.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.core.ui.ToastData
import com.sentyx.app.core.ui.ToastTone

/**
 * Full-screen bottom sheet. A dark scrim fills the screen and dismisses on tap;
 * the sheet is anchored to the bottom with a 22dp top radius and a drag handle.
 * Taps inside the sheet are consumed (they do not dismiss). [content] is a
 * column body below the handle.
 */
@Composable
fun SxBottomSheet(
    onDismiss: () -> Unit,
    modifier: Modifier = Modifier,
    content: @Composable () -> Unit,
) {
    val scrimInteraction = remember { MutableInteractionSource() }
    val sheetInteraction = remember { MutableInteractionSource() }
    Box(
        modifier = modifier
            .fillMaxSize()
            .background(Color(0x662B271F))
            .clickable(
                interactionSource = scrimInteraction,
                indication = null,
                onClick = onDismiss,
            ),
        contentAlignment = Alignment.BottomCenter,
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .clip(RoundedCornerShape(topStart = SxDimens.SheetRadius, topEnd = SxDimens.SheetRadius))
                .background(SxColors.Bg)
                // Consume taps so they don't reach the scrim.
                .clickable(
                    interactionSource = sheetInteraction,
                    indication = null,
                    onClick = {},
                )
                .padding(horizontal = 20.dp, vertical = 20.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Box(
                Modifier
                    .size(width = SxDimens.DragHandleWidth, height = SxDimens.DragHandleHeight)
                    .clip(RoundedCornerShape(2.dp))
                    .background(SxColors.Sand),
            )
            Column(Modifier.fillMaxWidth().padding(top = 16.dp)) { content() }
        }
    }
}

/** Pulsing-dot color for a toast tone. */
fun toastToneColor(tone: ToastTone): Color = when (tone) {
    ToastTone.Info -> SxColors.Bronze
    ToastTone.Success -> SxColors.Green
    ToastTone.Urgent -> SxColors.Red
}

/**
 * Floating dark toast banner shown at the top of the screen: a pulsing
 * tone-colored dot, a bold title with a muted subtitle, and an optional gold
 * CTA on the right. Tapping the bar invokes [ToastData.onTap]. The caller
 * positions this as a top overlay (the design insets it 12dp from each side).
 */
@Composable
fun SxToastBar(
    toast: ToastData,
    modifier: Modifier = Modifier,
) {
    Row(
        modifier = modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(16.dp))
            .background(SxColors.Ink)
            .clickable(enabled = toast.onTap != null) { toast.onTap?.invoke() }
            .padding(horizontal = 15.dp, vertical = 13.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        PulsingDot(color = toastToneColor(toast.tone), size = 9.dp)
        Column(Modifier.weight(1f)) {
            Text(toast.title, color = SxColors.OnInk, fontSize = 13.sp, fontWeight = FontWeight.Bold)
            if (toast.subtitle.isNotEmpty()) {
                Text(
                    toast.subtitle,
                    color = Color(0xFFC9C2B4),
                    fontSize = 11.5.sp,
                    modifier = Modifier.padding(top = 1.dp),
                )
            }
        }
        if (toast.cta.isNotEmpty()) {
            Text(toast.cta, color = SxColors.Gold, fontSize = 11.sp, fontWeight = FontWeight.Bold)
        }
    }
}

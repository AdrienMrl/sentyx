package com.sentyx.app.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/**
 * Display-only labeled input: a 12sp bold label above a bordered cream box
 * showing [value]. When [masked] is true the value is rendered as bullets with
 * wide letter-spacing (password look). This is a static mock — no editing.
 */
@Composable
fun SxTextFieldDisplay(
    label: String,
    value: String,
    modifier: Modifier = Modifier,
    masked: Boolean = false,
) {
    Column(modifier = modifier) {
        Text(
            label,
            color = SxColors.Muted,
            fontSize = 12.sp,
            fontWeight = FontWeight.Bold,
            modifier = Modifier.padding(bottom = 6.dp),
        )
        val shown = if (masked) "•".repeat(value.length.coerceAtLeast(8)) else value
        Text(
            text = shown,
            color = Color4A4436,
            fontSize = 14.sp,
            style = if (masked) TextStyle(letterSpacing = 3.sp) else TextStyle.Default,
            modifier = Modifier
                .fillMaxWidth()
                .clip(RoundedCornerShape(SxDimens.InputRadius))
                .background(SxColors.Card)
                .border(1.dp, SxColors.Border, RoundedCornerShape(SxDimens.InputRadius))
                .padding(horizontal = 14.dp, vertical = 13.dp),
        )
    }
}

private val Color4A4436 = androidx.compose.ui.graphics.Color(0xFF4A4436)

package com.sentyx.app.core.designsystem

import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/** 38x22 pill toggle: green when on, sand when off, white 18dp knob. */
@Composable
fun SxToggle(
    checked: Boolean,
    onToggle: (Boolean) -> Unit,
    modifier: Modifier = Modifier,
) {
    val trackColor by animateColorAsState(if (checked) SxColors.Green else SxColors.Sand)
    val knobOffset by animateDpAsState(if (checked) 18.dp else 2.dp)
    Box(
        modifier = modifier
            .width(SxDimens.ToggleWidth)
            .clip(RoundedCornerShape(11.dp))
            .background(trackColor)
            .clickable { onToggle(!checked) }
            .padding(vertical = 2.dp),
        contentAlignment = Alignment.CenterStart,
    ) {
        Box(
            Modifier
                .padding(start = knobOffset - 2.dp)
                .size(SxDimens.ToggleKnob)
                .clip(CircleShape)
                .background(SxColors.White),
        )
    }
}

/** Settings row with a title (+ optional subtitle) and a trailing [SxToggle]. */
@Composable
fun SxToggleRow(
    title: String,
    checked: Boolean,
    onToggle: (Boolean) -> Unit,
    modifier: Modifier = Modifier,
    subtitle: String? = null,
) {
    Row(
        modifier = modifier
            .clickable { onToggle(!checked) }
            .padding(horizontal = SxDimens.ListRowHPadding, vertical = SxDimens.ListRowVPadding),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.SpaceBetween,
    ) {
        Column(Modifier.weight(1f)) {
            Text(title, color = SxColors.Ink, fontSize = 13.5.sp, fontWeight = FontWeight.SemiBold)
            if (subtitle != null) {
                Text(subtitle, color = SxColors.Muted, fontSize = 11.5.sp, modifier = Modifier.padding(top = 1.dp))
            }
        }
        SxToggle(checked = checked, onToggle = onToggle)
    }
}

/**
 * Pill chip. Selected = dark ink background / cream text; unselected = cream
 * background / muted text / 1px border.
 */
@Composable
fun SxChip(
    label: String,
    selected: Boolean,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val bg = if (selected) SxColors.Ink else SxColors.Card
    val fg = if (selected) SxColors.OnInk else SxColors.InkSecondary
    val border = if (selected) SxColors.Ink else SxColors.Border
    Box(
        modifier = modifier
            .clip(RoundedCornerShape(SxDimens.ChipRadius))
            .background(bg)
            .border(1.dp, border, RoundedCornerShape(SxDimens.ChipRadius))
            .clickable(onClick = onClick)
            .padding(horizontal = 13.dp, vertical = 7.dp),
    ) {
        Text(label, color = fg, fontSize = 12.sp, fontWeight = FontWeight.Bold)
    }
}

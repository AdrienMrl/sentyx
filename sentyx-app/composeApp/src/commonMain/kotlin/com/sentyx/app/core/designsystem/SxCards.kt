package com.sentyx.app.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/**
 * Card surface: cream background, 1px border, 16dp radius. Optionally clickable.
 * [content] renders inside the padded card body.
 */
@Composable
fun SxCard(
    modifier: Modifier = Modifier,
    onClick: (() -> Unit)? = null,
    content: @Composable () -> Unit,
) {
    val shape = RoundedCornerShape(SxDimens.CardRadius)
    var m = modifier
        .clip(shape)
        .background(SxColors.Card)
        .border(1.dp, SxColors.Border, shape)
    if (onClick != null) m = m.clickable(onClick = onClick)
    Box(modifier = m.padding(14.dp)) { content() }
}

/**
 * Grouped settings-list container: a bordered card that clips its rows. Pair
 * with [SxListRow]; the group draws the outer border, so rows are edge-to-edge.
 */
@Composable
fun SxListGroup(
    modifier: Modifier = Modifier,
    content: @Composable () -> Unit,
) {
    val shape = RoundedCornerShape(SxDimens.CardRadiusSmall)
    Column(
        modifier = modifier
            .fillMaxWidth()
            .clip(shape)
            .background(SxColors.Card)
            .border(1.dp, SxColors.Border, shape),
    ) {
        content()
    }
}

/**
 * One row of a settings list. Renders [label] (with optional [labelColor]),
 * an optional [value] on the right (with optional [valueColor]), an optional
 * [showChevron], and an optional [trailing] slot rendered before the chevron.
 * Pass [showDivider] = false on the last row of a group.
 */
@Composable
fun SxListRow(
    label: String,
    modifier: Modifier = Modifier,
    value: String? = null,
    onClick: (() -> Unit)? = null,
    showChevron: Boolean = false,
    showDivider: Boolean = true,
    labelColor: Color = SxColors.Ink,
    valueColor: Color = SxColors.Muted,
    trailing: (@Composable () -> Unit)? = null,
) {
    var m = modifier.fillMaxWidth()
    if (onClick != null) m = m.clickable(onClick = onClick)
    Column(modifier = m) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = SxDimens.ListRowHPadding, vertical = SxDimens.ListRowVPadding),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween,
        ) {
            Text(
                label,
                color = labelColor,
                fontSize = 13.5.sp,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier.weight(1f, fill = false),
            )
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                when {
                    trailing != null -> trailing()
                    value != null -> Text(value, color = valueColor, fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
                }
                if (showChevron) Text("›", color = SxColors.Chevron, fontSize = 16.sp)
            }
        }
        if (showDivider) {
            Box(
                Modifier
                    .fillMaxWidth()
                    .height(1.dp)
                    .background(SxColors.Divider),
            )
        }
    }
}

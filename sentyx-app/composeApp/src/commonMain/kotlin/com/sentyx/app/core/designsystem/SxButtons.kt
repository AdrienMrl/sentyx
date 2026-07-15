package com.sentyx.app.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/** Dark ink pill, the app's primary call to action. Full-width by default. */
@Composable
fun SxPrimaryButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Box(
        modifier = modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(SxDimens.ButtonRadius))
            .background(SxColors.Ink)
            .clickable(onClick = onClick)
            .padding(vertical = SxDimens.PrimaryButtonVPadding),
        contentAlignment = Alignment.Center,
    ) {
        Text(text, color = SxColors.OnInk, fontSize = 15.sp, fontWeight = FontWeight.Bold, textAlign = TextAlign.Center)
    }
}

/** Transparent pill with a 1px sand border. */
@Composable
fun SxSecondaryButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Box(
        modifier = modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(SxDimens.ButtonRadius))
            .border(1.dp, SxColors.Sand, RoundedCornerShape(SxDimens.ButtonRadius))
            .clickable(onClick = onClick)
            .padding(vertical = SxDimens.SecondaryButtonVPadding),
        contentAlignment = Alignment.Center,
    ) {
        Text(text, color = SxColors.Ink, fontSize = 15.sp, fontWeight = FontWeight.Bold, textAlign = TextAlign.Center)
    }
}

/** Bronze, bold inline text action. */
@Composable
fun SxTextLink(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Text(
        text = text,
        color = SxColors.Bronze,
        fontSize = 13.sp,
        fontWeight = FontWeight.Bold,
        modifier = modifier.clickable(onClick = onClick),
    )
}

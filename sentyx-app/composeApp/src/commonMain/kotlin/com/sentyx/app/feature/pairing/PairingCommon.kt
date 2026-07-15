package com.sentyx.app.feature.pairing

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxDimens

// Onboarding-specific frame metrics (top ~70dp, horizontal 26dp, bottom 30dp).
private val OnboardingTopPadding = 70.dp
private val OnboardingBottomPadding = 30.dp

/** Text color for filled-in field values (warmer than pure ink). */
internal val FieldInk = Color(0xFF4A4436)

/**
 * Full-height onboarding page frame: cream background with the standard
 * onboarding padding. [content] is a column body; screens add their own
 * `Spacer(Modifier.weight(1f))` to push footers down.
 */
@Composable
internal fun OnboardingScreen(
    modifier: Modifier = Modifier,
    content: @Composable ColumnScope.() -> Unit,
) {
    Column(
        modifier = modifier
            .fillMaxSize()
            .background(SxColors.Bg)
            .padding(
                start = SxDimens.OnboardingPadding,
                end = SxDimens.OnboardingPadding,
                top = OnboardingTopPadding,
                bottom = OnboardingBottomPadding,
            ),
        content = content,
    )
}

/** Standalone "‹" back glyph used at the top of an onboarding screen. */
@Composable
internal fun OnboardingBack(onBack: () -> Unit, modifier: Modifier = Modifier) {
    Text(
        "‹",
        color = SxColors.Muted,
        fontSize = 22.sp,
        modifier = modifier
            .width(30.dp)
            .clickable(onClick = onBack),
    )
}

/** 28sp extrabold onboarding title. */
@Composable
internal fun OnboardingTitle(text: String, modifier: Modifier = Modifier) {
    Text(
        text,
        color = SxColors.Ink,
        fontSize = 28.sp,
        fontWeight = FontWeight.ExtraBold,
        style = TextStyle(letterSpacing = (-0.5).sp),
        modifier = modifier,
    )
}

/** Muted lead paragraph under an onboarding title. */
@Composable
internal fun OnboardingSubtitle(text: String, modifier: Modifier = Modifier) {
    Text(
        text,
        color = SxColors.InkSecondary,
        fontSize = 14.sp,
        lineHeight = 21.sp,
        modifier = modifier,
    )
}

/** Rounded 34dp tile holding an emoji/glyph, as used by checklist/permission rows. */
@Composable
internal fun IconTile(
    glyph: String,
    modifier: Modifier = Modifier,
    tileSize: androidx.compose.ui.unit.Dp = 34.dp,
    background: Color = SxColors.Divider,
) {
    Box(
        modifier = modifier
            .size(tileSize)
            .clip(RoundedCornerShape(10.dp))
            .background(background),
        contentAlignment = Alignment.Center,
    ) {
        Text(glyph, fontSize = 16.sp)
    }
}

/**
 * Editable labeled field styled like [com.sentyx.app.core.designsystem.SxTextFieldDisplay]
 * but backed by a real text field. Used for the device name, nickname and
 * Wi-Fi password inputs.
 */
@Composable
internal fun PairEditField(
    label: String,
    value: String,
    onValueChange: (String) -> Unit,
    modifier: Modifier = Modifier,
    placeholder: String = "",
    masked: Boolean = false,
    optional: Boolean = false,
) {
    Column(modifier = modifier) {
        FieldLabel(label = label, optional = optional)
        val shape = RoundedCornerShape(SxDimens.InputRadius)
        BasicTextField(
            value = value,
            onValueChange = onValueChange,
            singleLine = true,
            textStyle = TextStyle(
                color = FieldInk,
                fontSize = 14.sp,
                fontWeight = FontWeight.SemiBold,
                letterSpacing = if (masked) 3.sp else 0.sp,
            ),
            cursorBrush = SolidColor(SxColors.Bronze),
            visualTransformation = if (masked) PasswordVisualTransformation() else VisualTransformation.None,
            modifier = Modifier
                .fillMaxWidth()
                .clip(shape)
                .background(SxColors.Card)
                .border(1.dp, SxColors.Border, shape)
                .padding(horizontal = 14.dp, vertical = 13.dp),
            decorationBox = { inner ->
                if (value.isEmpty() && placeholder.isNotEmpty()) {
                    Text(placeholder, color = SxColors.Hint, fontSize = 14.sp, fontWeight = FontWeight.SemiBold)
                }
                inner()
            },
        )
    }
}

@Composable
private fun FieldLabel(label: String, optional: Boolean) {
    Column {
        androidx.compose.foundation.layout.Row {
            Text(label, color = SxColors.Muted, fontSize = 12.sp, fontWeight = FontWeight.Bold)
            if (optional) {
                Text(
                    "  (optional)",
                    color = SxColors.Faint,
                    fontSize = 12.sp,
                    fontWeight = FontWeight.Medium,
                )
            }
        }
        Box(Modifier.size(6.dp))
    }
}

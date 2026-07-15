package com.sentyx.app.feature.onboarding

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
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.core.designsystem.SentyxLogo
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxDimens
import com.sentyx.app.core.designsystem.SxSpinner
import com.sentyx.app.core.designsystem.monoFamily

/** Text color the design uses inside filled input boxes. */
private val InputInk = Color(0xFF4A4436)

/**
 * Editable labeled input styled to match `SxTextFieldDisplay`'s box look: a 12sp
 * bold label above a bordered cream box. Unlike the display-only component this
 * accepts real keystrokes; [masked] renders bullets for passwords.
 */
@Composable
fun AuthTextField(
    label: String,
    value: String,
    onValueChange: (String) -> Unit,
    modifier: Modifier = Modifier,
    masked: Boolean = false,
    keyboardType: KeyboardType = KeyboardType.Text,
) {
    Column(modifier = modifier, verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(
            label,
            color = SxColors.Muted,
            fontSize = 12.sp,
            fontWeight = FontWeight.Bold,
        )
        BasicTextField(
            value = value,
            onValueChange = onValueChange,
            singleLine = true,
            textStyle = TextStyle(
                color = InputInk,
                fontSize = 14.sp,
                letterSpacing = if (masked) 3.sp else TextStyle.Default.letterSpacing,
            ),
            visualTransformation = if (masked) PasswordVisualTransformation() else androidx.compose.ui.text.input.VisualTransformation.None,
            keyboardOptions = KeyboardOptions(keyboardType = keyboardType),
            cursorBrush = SolidColor(SxColors.Bronze),
            modifier = Modifier
                .fillMaxWidth()
                .clip(RoundedCornerShape(SxDimens.InputRadius))
                .background(SxColors.Card)
                .border(1.dp, SxColors.Border, RoundedCornerShape(SxDimens.InputRadius))
                .padding(horizontal = 14.dp, vertical = 13.dp),
        )
    }
}

/**
 * Dark ink primary CTA that mirrors `SxPrimaryButton` but shows a spinner and
 * ignores taps while [loading].
 */
@Composable
fun AuthPrimaryButton(
    text: String,
    loading: Boolean,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Box(
        modifier = modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(SxDimens.ButtonRadius))
            .background(SxColors.Ink)
            .clickable(enabled = !loading, onClick = onClick)
            .padding(vertical = SxDimens.PrimaryButtonVPadding),
        contentAlignment = Alignment.Center,
    ) {
        if (loading) {
            SxSpinner(size = 20.dp, strokeWidth = 2.dp)
        } else {
            Text(
                text,
                color = SxColors.OnInk,
                fontSize = 15.sp,
                fontWeight = FontWeight.Bold,
                textAlign = TextAlign.Center,
            )
        }
    }
}

/** Standalone "‹" back glyph used at the top of the onboarding sub-screens. */
@Composable
fun BackChevron(onBack: () -> Unit, modifier: Modifier = Modifier) {
    Text(
        "‹",
        color = SxColors.Muted,
        fontSize = 22.sp,
        modifier = modifier
            .width(30.dp)
            .clickable(onClick = onBack),
    )
}

/** The gold Sentyx mark tile, reused from the design system at its default size. */
@Composable
fun AuthLogo(modifier: Modifier = Modifier) = SentyxLogo(modifier = modifier)

/**
 * Six OTP cells that display [code]'s digits in IBM Plex Mono. A transparent
 * full-width text field lies on top so tapping anywhere focuses editing; typed
 * digits flow back through [onCodeChange].
 */
@Composable
fun CodeCells(
    code: String,
    onCodeChange: (String) -> Unit,
    modifier: Modifier = Modifier,
) {
    Box(modifier = modifier) {
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            repeat(6) { i ->
                val ch = code.getOrNull(i)?.toString() ?: ""
                Box(
                    modifier = Modifier
                        .size(width = 38.dp, height = 48.dp)
                        .clip(RoundedCornerShape(11.dp))
                        .background(SxColors.Card)
                        .border(
                            1.dp,
                            if (ch.isNotEmpty()) SxColors.Sand else SxColors.Border,
                            RoundedCornerShape(11.dp),
                        ),
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        ch,
                        color = SxColors.Ink,
                        fontSize = 20.sp,
                        fontWeight = FontWeight.Bold,
                        fontFamily = monoFamily,
                    )
                }
            }
        }
        // Invisible capture field spanning the cells.
        BasicTextField(
            value = code,
            onValueChange = onCodeChange,
            singleLine = true,
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
            cursorBrush = SolidColor(Color.Transparent),
            textStyle = TextStyle(color = Color.Transparent),
            modifier = Modifier
                .matchParentSize()
                .alpha(0f),
        )
    }
}

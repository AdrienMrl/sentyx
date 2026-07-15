package com.sentyx.app.core.designsystem

import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp

/** A small dot that pulses its alpha 1 → 0.3 → 1 on a ~1.6s loop. */
@Composable
fun PulsingDot(
    color: Color,
    modifier: Modifier = Modifier,
    size: Dp = 8.dp,
) {
    val transition = rememberInfiniteTransition()
    val alpha by transition.animateFloat(
        initialValue = 1f,
        targetValue = 0.3f,
        animationSpec = infiniteRepeatable(
            animation = tween(800, easing = LinearEasing),
            repeatMode = RepeatMode.Reverse,
        ),
    )
    Box(
        modifier
            .size(size)
            .alpha(alpha)
            .clip(CircleShape)
            .background(color),
    )
}

/**
 * Circular border spinner: a track ring with a bronze top arc, rotating once
 * per second. Track is [SxColors.Border], arc is [SxColors.Bronze].
 */
@Composable
fun SxSpinner(
    modifier: Modifier = Modifier,
    size: Dp = 44.dp,
    strokeWidth: Dp = 3.dp,
) {
    val transition = rememberInfiniteTransition()
    val angle by transition.animateFloat(
        initialValue = 0f,
        targetValue = 360f,
        animationSpec = infiniteRepeatable(
            animation = tween(1000, easing = LinearEasing),
            repeatMode = RepeatMode.Restart,
        ),
    )
    Canvas(modifier.size(size).rotate(angle)) {
        val stroke = strokeWidth.toPx()
        val inset = stroke / 2f
        val arcSize = Size(this.size.width - stroke, this.size.height - stroke)
        val topLeft = Offset(inset, inset)
        drawArc(
            color = SxColors.Border,
            startAngle = 0f,
            sweepAngle = 360f,
            useCenter = false,
            topLeft = topLeft,
            size = arcSize,
            style = Stroke(width = stroke),
        )
        drawArc(
            color = SxColors.Bronze,
            startAngle = -90f,
            sweepAngle = 90f,
            useCenter = false,
            topLeft = topLeft,
            size = arcSize,
            style = Stroke(width = stroke, cap = StrokeCap.Round),
        )
    }
}

/** Thin progress bar: a 5dp divider-colored track with a colored fill. */
@Composable
fun SxProgressBar(
    pct: Float,
    color: Color,
    modifier: Modifier = Modifier,
    height: Dp = 5.dp,
) {
    val clamped = pct.coerceIn(0f, 1f)
    val shape = RoundedCornerShape(height / 2)
    Box(
        modifier
            .fillMaxWidth()
            .height(height)
            .clip(shape)
            .background(SxColors.Divider),
    ) {
        Box(
            Modifier
                .fillMaxWidth(clamped)
                .height(height)
                .clip(shape)
                .background(color),
        )
    }
}

/**
 * Diagonal-striped placeholder used for video thumbnails: repeating 45° stripes
 * alternating between two warm tones. An optional [overlay] slot is centered.
 */
@Composable
fun StripedThumb(
    modifier: Modifier = Modifier,
    overlay: (@Composable () -> Unit)? = null,
) {
    val stripeA = Color(0xFFE3DCCD)
    val stripeB = Color(0xFFEBE5D8)
    Box(
        modifier = modifier.drawBehind {
            drawRect(stripeB)
            val stripe = 6.dp.toPx()
            val period = stripe * 2f
            // Diagonal (45°) bands: shift each row so stripes read as a diagonal.
            val diag = size.width + size.height
            var offset = -size.height
            while (offset < diag) {
                val path = androidx.compose.ui.graphics.Path().apply {
                    moveTo(offset, 0f)
                    lineTo(offset + stripe, 0f)
                    lineTo(offset + stripe - size.height, size.height)
                    lineTo(offset - size.height, size.height)
                    close()
                }
                drawPath(path, stripeA)
                offset += period
            }
        },
        contentAlignment = Alignment.Center,
    ) {
        if (overlay != null) overlay()
    }
}

/**
 * The Sentyx mark: a gold solid inner circle stroke plus a dashed outer circle,
 * on a dark rounded-square tile. [size] is the tile edge.
 */
@Composable
fun SentyxLogo(
    modifier: Modifier = Modifier,
    size: Dp = 60.dp,
) {
    val corner = size * 0.28f
    Box(
        modifier = modifier
            .size(size)
            .clip(RoundedCornerShape(corner))
            .background(SxColors.Ink),
        contentAlignment = Alignment.Center,
    ) {
        Canvas(Modifier.size(size * 0.5f)) {
            val stroke = this.size.minDimension * 0.08f
            val center = Offset(this.size.width / 2f, this.size.height / 2f)
            val outerR = this.size.minDimension * 0.4f
            val innerR = this.size.minDimension * 0.2f
            drawCircle(
                color = SxColors.Gold,
                radius = innerR,
                center = center,
                style = Stroke(width = stroke),
            )
            drawCircle(
                color = SxColors.Gold,
                radius = outerR,
                center = center,
                style = Stroke(
                    width = stroke,
                    pathEffect = PathEffect.dashPathEffect(floatArrayOf(stroke * 1.6f, stroke * 2f)),
                ),
            )
        }
    }
}

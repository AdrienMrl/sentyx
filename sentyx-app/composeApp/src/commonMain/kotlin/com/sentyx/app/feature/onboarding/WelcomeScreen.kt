package com.sentyx.app.feature.onboarding

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxPrimaryButton
import com.sentyx.app.core.designsystem.SxSecondaryButton

/**
 * Landing screen: Sentyx mark, headline, body copy, and the two entry CTAs.
 */
@Composable
fun WelcomeScreen(
    onCreateAccount: () -> Unit,
    onSignIn: () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(start = 26.dp, end = 26.dp, top = 78.dp, bottom = 30.dp),
    ) {
        Column(
            modifier = Modifier.weight(1f),
            verticalArrangement = Arrangement.spacedBy(22.dp, Alignment.CenterVertically),
        ) {
            AuthLogo()
            Column {
                Text(
                    "SENTYX",
                    color = SxColors.Muted,
                    fontSize = 13.sp,
                    fontWeight = FontWeight.Bold,
                    style = TextStyle(letterSpacing = 3.sp),
                )
                Text(
                    "Your car,\nalways watched.",
                    color = SxColors.Ink,
                    fontSize = 34.sp,
                    fontWeight = FontWeight.ExtraBold,
                    lineHeight = 36.sp,
                    style = TextStyle(letterSpacing = (-1).sp),
                    modifier = Modifier.padding(top = 10.dp),
                )
                Text(
                    "Turn Sentry Mode into a live feed. Sentyx catches events as your Tesla records them, and explains what happened with on-device AI.",
                    color = SxColors.InkSecondary,
                    fontSize = 14.sp,
                    lineHeight = 22.sp,
                    modifier = Modifier.padding(top = 14.dp),
                )
            }
        }
        Column(verticalArrangement = Arrangement.spacedBy(11.dp)) {
            SxPrimaryButton(text = "Create account", onClick = onCreateAccount)
            SxSecondaryButton(text = "Sign in", onClick = onSignIn)
            Text(
                buildAnnotatedString {
                    append("By continuing you accept the ")
                    withStyle(SpanStyle(color = SxColors.Bronze, fontWeight = FontWeight.SemiBold)) {
                        append("Terms")
                    }
                    append(" & ")
                    withStyle(SpanStyle(color = SxColors.Bronze, fontWeight = FontWeight.SemiBold)) {
                        append("Privacy Policy")
                    }
                    append(".")
                },
                color = SxColors.Faint,
                fontSize = 11.sp,
                lineHeight = 16.5.sp,
                textAlign = TextAlign.Center,
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(top = 6.dp),
            )
        }
    }
}

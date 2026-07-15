package com.sentyx.app.feature.onboarding

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.core.designsystem.SxColors

/**
 * Email verification: envelope tile, six OTP cells (prefilled with the demo
 * code), Resend link, and the Verify & continue CTA.
 */
@Composable
fun VerifyEmailScreen(
    vm: AuthViewModel,
    onBack: () -> Unit,
    onVerified: () -> Unit,
) {
    val state by vm.state.collectAsState()
    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(start = 26.dp, end = 26.dp, top = 70.dp, bottom = 30.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        BackChevron(onBack = onBack, modifier = Modifier.align(Alignment.Start))
        Column(
            modifier = Modifier.weight(1f),
            verticalArrangement = Arrangement.spacedBy(16.dp, Alignment.CenterVertically),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Box(
                modifier = Modifier
                    .size(56.dp)
                    .clip(RoundedCornerShape(16.dp))
                    .background(SxColors.Card)
                    .border(1.dp, SxColors.Border, RoundedCornerShape(16.dp)),
                contentAlignment = Alignment.Center,
            ) {
                Text("✉", fontSize = 24.sp)
            }
            Text(
                "Verify your email",
                color = SxColors.Ink,
                fontSize = 24.sp,
                fontWeight = FontWeight.ExtraBold,
                style = TextStyle(letterSpacing = (-0.4).sp),
            )
            Text(
                buildAnnotatedString {
                    append("We sent a 6-digit code to ")
                    withStyle(SpanStyle(color = SxColors.Ink, fontWeight = FontWeight.Bold)) {
                        append(state.email)
                    }
                    append(". Enter it to confirm your account.")
                },
                color = SxColors.InkSecondary,
                fontSize = 14.sp,
                lineHeight = 22.sp,
                textAlign = TextAlign.Center,
                modifier = Modifier.widthIn(max = 280.dp),
            )
            CodeCells(
                code = state.code,
                onCodeChange = vm::updateCode,
                modifier = Modifier.padding(top = 6.dp),
            )
            Text(
                buildAnnotatedString {
                    append("Didn't get it? ")
                    withStyle(SpanStyle(color = SxColors.Bronze, fontWeight = FontWeight.SemiBold)) {
                        append("Resend")
                    }
                },
                color = SxColors.Faint,
                fontSize = 12.5.sp,
                modifier = Modifier.clickable(onClick = vm::resendCode),
            )
        }
        AuthPrimaryButton(
            text = "Verify & continue",
            loading = state.loading,
            onClick = { vm.verifyEmail(onVerified) },
            modifier = Modifier.fillMaxWidth(),
        )
    }
}

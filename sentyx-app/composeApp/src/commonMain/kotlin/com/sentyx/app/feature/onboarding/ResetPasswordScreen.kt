package com.sentyx.app.feature.onboarding

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.core.designsystem.SxColors

/**
 * Reset-password screen: single email field; Send reset link runs
 * [AuthViewModel.sendPasswordReset], which shows a success toast then navigates
 * back via [onBack].
 */
@Composable
fun ResetPasswordScreen(
    vm: AuthViewModel,
    onBack: () -> Unit,
) {
    val state by vm.state.collectAsState()
    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(start = 26.dp, end = 26.dp, top = 70.dp, bottom = 30.dp),
    ) {
        BackChevron(onBack = onBack)
        Text(
            "Reset password",
            color = SxColors.Ink,
            fontSize = 28.sp,
            fontWeight = FontWeight.ExtraBold,
            style = TextStyle(letterSpacing = (-0.5).sp),
            modifier = Modifier.padding(top = 14.dp),
        )
        Text(
            "Enter your email and we'll send a reset link.",
            color = SxColors.InkSecondary,
            fontSize = 14.sp,
            lineHeight = 22.sp,
            modifier = Modifier.padding(top = 10.dp),
        )
        AuthTextField(
            label = "Email",
            value = state.email,
            onValueChange = vm::updateEmail,
            keyboardType = KeyboardType.Email,
            modifier = Modifier.padding(top = 20.dp),
        )
        Spacer(Modifier.weight(1f))
        AuthPrimaryButton(
            text = "Send reset link",
            loading = state.loading,
            onClick = { vm.sendPasswordReset(onBack) },
        )
    }
}

package com.sentyx.app.feature.onboarding

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.core.designsystem.SxColors

/**
 * Sign-in screen: email + password, forgot-password link, and the Sign in CTA
 * that runs [AuthViewModel.signIn] then navigates on success.
 */
@Composable
fun SignInScreen(
    vm: AuthViewModel,
    onBack: () -> Unit,
    onForgotPassword: () -> Unit,
    onSignedIn: () -> Unit,
) {
    val state by vm.state.collectAsState()
    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(start = 26.dp, end = 26.dp, top = 70.dp, bottom = 30.dp),
    ) {
        BackChevron(onBack = onBack)
        Text(
            "Welcome back",
            color = SxColors.Ink,
            fontSize = 28.sp,
            fontWeight = FontWeight.ExtraBold,
            style = TextStyle(letterSpacing = (-0.5).sp),
            modifier = Modifier.padding(top = 14.dp),
        )
        Column(
            modifier = Modifier.padding(top = 22.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            AuthTextField(
                label = "Email",
                value = state.email,
                onValueChange = vm::updateEmail,
                keyboardType = KeyboardType.Email,
            )
            AuthTextField(
                label = "Password",
                value = state.password,
                onValueChange = vm::updatePassword,
                masked = true,
                keyboardType = KeyboardType.Password,
            )
            Text(
                "Forgot password?",
                color = SxColors.Bronze,
                fontSize = 12.5.sp,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier
                    .align(Alignment.End)
                    .clickable(onClick = onForgotPassword),
            )
        }
        Spacer(Modifier.weight(1f))
        AuthPrimaryButton(
            text = "Sign in",
            loading = state.loading,
            onClick = { vm.signIn(onSignedIn) },
        )
    }
}

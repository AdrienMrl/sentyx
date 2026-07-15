package com.sentyx.app.feature.pairing

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxPrimaryButton

/** Success screen: the device is live; enter the app. */
@Composable
fun PairingDoneScreen(
    vm: PairingViewModel,
    onEnterApp: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()
    // Reaching this screen means the flow finished — mark the device ready.
    LaunchedEffect(Unit) { vm.completePairing() }
    OnboardingScreen {
        Column(
            modifier = Modifier.weight(1f).fillMaxWidth(),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.Center,
        ) {
            Box(
                modifier = Modifier
                    .size(74.dp)
                    .clip(CircleShape)
                    .background(SxColors.Green),
                contentAlignment = Alignment.Center,
            ) {
                Text("✓", color = SxColors.White, fontSize = 36.sp)
            }
            Text(
                "${state.deviceName} is live",
                color = SxColors.Ink,
                fontSize = 26.sp,
                fontWeight = FontWeight.ExtraBold,
                style = TextStyle(letterSpacing = (-0.5).sp),
                textAlign = TextAlign.Center,
                modifier = Modifier.padding(top = 18.dp),
            )
            Text(
                "Your ${state.vehicleModel} is now monitored. New Sentry events will appear here with AI summaries.",
                color = SxColors.InkSecondary,
                fontSize = 14.sp,
                lineHeight = 22.sp,
                textAlign = TextAlign.Center,
                modifier = Modifier.padding(top = 18.dp).widthIn(max = 280.dp),
            )
        }
        SxPrimaryButton("Go to events", onEnterApp)
    }
}

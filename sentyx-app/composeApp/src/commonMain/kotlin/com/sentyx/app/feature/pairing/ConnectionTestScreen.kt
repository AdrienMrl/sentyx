package com.sentyx.app.feature.pairing

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SxCard
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxPrimaryButton
import com.sentyx.app.domain.model.ConnectionTestStep

/** Runs the connection test; rows appear with green checks as they complete. */
@Composable
fun ConnectionTestScreen(
    vm: PairingViewModel,
    onContinue: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()
    LaunchedEffect(Unit) { vm.runConnectionTest() }

    OnboardingScreen {
        OnboardingTitle("Testing connection")
        Column(
            modifier = Modifier.padding(top = 24.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            state.connectionTest.forEach { step -> TestRow(step) }
        }
        Spacer(Modifier.weight(1f))
        SxPrimaryButton("Continue", onContinue)
    }
}

@Composable
private fun TestRow(step: ConnectionTestStep) {
    SxCard {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(13.dp),
        ) {
            Box(
                modifier = Modifier
                    .size(24.dp)
                    .clip(CircleShape)
                    .background(if (step.passed) SxColors.Green else SxColors.Sand),
                contentAlignment = Alignment.Center,
            ) {
                Text(
                    if (step.passed) "✓" else "…",
                    color = SxColors.White,
                    fontSize = 13.sp,
                    fontWeight = FontWeight.Bold,
                )
            }
            Column(Modifier.weight(1f)) {
                Text(step.title, color = SxColors.Ink, fontSize = 14.sp, fontWeight = FontWeight.Bold)
                Text(step.subtitle, color = SxColors.Muted, fontSize = 12.sp, modifier = Modifier.padding(top = 1.dp))
            }
        }
    }
}

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

    val failed = state.connectionTest.any { !it.passed }
    val passed = !state.testRunning && !failed && state.connectionTest.isNotEmpty()

    OnboardingScreen {
        OnboardingTitle("Testing connection")
        Column(
            modifier = Modifier.padding(top = 24.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            state.connectionTest.forEach { step -> TestRow(step) }
            if (state.testRunning) TestPendingNote()
        }
        Spacer(Modifier.weight(1f))
        // Advancing is gated on a PASSED test, because the next step tells the
        // device to complete onboarding and the device refuses that without one
        // ("complete requires a passed connection test"). This button used to be
        // live throughout: tapping it while the test was still running — or
        // after it had failed — walked the user to a success screen while the
        // unit stayed unprovisioned, with the real reason only in the Pi's log.
        when {
            state.testRunning -> SxPrimaryButton("Testing…", {}, enabled = false)
            failed -> SxPrimaryButton("Try again", { vm.runConnectionTest() })
            else -> SxPrimaryButton("Continue", onContinue, enabled = passed)
        }
    }
}

/**
 * Shown while the test is in flight. The first check can take a while on a unit
 * that has just been switched on — it waits for the clock to be set before
 * anything can be verified over HTTPS — and an unexplained pause reads as a
 * freeze.
 */
@Composable
private fun TestPendingNote() {
    Text(
        "Checking the device's connection to Sentyx. This can take up to a minute " +
            "on a device that has just been powered on.",
        color = SxColors.Muted,
        fontSize = 13.sp,
        modifier = Modifier.padding(top = 4.dp),
    )
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

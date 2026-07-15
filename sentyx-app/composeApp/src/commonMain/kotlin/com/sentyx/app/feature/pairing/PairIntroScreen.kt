package com.sentyx.app.feature.pairing

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SxCard
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxPrimaryButton

/** Pairing intro: hardware checklist before starting the flow. */
@Composable
fun PairIntroScreen(
    vm: PairingViewModel,
    onBack: () -> Unit,
    onContinue: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()
    OnboardingScreen {
        OnboardingBack(onBack)
        OnboardingTitle("Let's pair your Sentyx Pi", Modifier.padding(top = 14.dp))
        OnboardingSubtitle("A few things you'll need before we start:", Modifier.padding(top = 10.dp))
        Column(
            modifier = Modifier.padding(top = 20.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            state.hardware.forEach { req ->
                SxCard {
                    Row(horizontalArrangement = Arrangement.spacedBy(13.dp)) {
                        IconTile(req.icon)
                        Column {
                            Text(req.title, color = SxColors.Ink, fontSize = 14.sp, fontWeight = FontWeight.Bold)
                            Text(
                                req.subtitle,
                                color = SxColors.Muted,
                                fontSize = 12.sp,
                                lineHeight = 17.sp,
                                modifier = Modifier.padding(top = 2.dp),
                            )
                        }
                    }
                }
            }
        }
        Spacer(Modifier.weight(1f))
        SxPrimaryButton("Start pairing", onContinue)
    }
}

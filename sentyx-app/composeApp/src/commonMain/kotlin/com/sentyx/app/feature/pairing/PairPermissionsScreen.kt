package com.sentyx.app.feature.pairing

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxDimens
import com.sentyx.app.core.designsystem.SxPrimaryButton
import com.sentyx.app.domain.model.OnboardingPermission

/** Permission grants needed to discover and talk to the Pi. */
@Composable
fun PairPermissionsScreen(
    vm: PairingViewModel,
    onBack: () -> Unit,
    onContinue: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()
    OnboardingScreen {
        OnboardingBack(onBack)
        OnboardingTitle("Permissions", Modifier.padding(top = 14.dp))
        OnboardingSubtitle("Sentyx needs these to find and talk to your Pi.", Modifier.padding(top = 10.dp))
        Column(
            modifier = Modifier.padding(top = 20.dp),
            verticalArrangement = Arrangement.spacedBy(11.dp),
        ) {
            state.permissions.forEach { perm ->
                PermissionRow(perm, onTap = { vm.requestPermission(perm.key) })
            }
        }
        Text(
            "Bluetooth is required to find and pair your Pi. Denied a permission? You can " +
                "enable it later in Settings → App permissions.",
            color = SxColors.Faint,
            fontSize = 11.5.sp,
            lineHeight = 17.sp,
            modifier = Modifier.padding(top = 14.dp),
        )
        Spacer(Modifier.weight(1f))
        val canContinue = state.canProceedFromPermissions
        SxPrimaryButton(
            text = "Continue",
            onClick = { if (canContinue) onContinue() },
            modifier = Modifier.alpha(if (canContinue) 1f else 0.4f),
        )
    }
}

@Composable
private fun PermissionRow(perm: OnboardingPermission, onTap: () -> Unit) {
    val borderColor = if (perm.granted) SxColors.Green.copy(alpha = 0.4f) else SxColors.Border
    val shape = RoundedCornerShape(SxDimens.CardRadiusSmall)
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clip(shape)
            .background(SxColors.Card)
            .border(1.dp, borderColor, shape)
            .clickable(onClick = onTap)
            .padding(14.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(13.dp),
    ) {
        IconTile(perm.icon)
        Column(Modifier.weight(1f)) {
            Text(perm.title, color = SxColors.Ink, fontSize = 14.sp, fontWeight = FontWeight.Bold)
            Text(
                perm.subtitle,
                color = SxColors.Muted,
                fontSize = 12.sp,
                lineHeight = 17.sp,
                modifier = Modifier.padding(top = 2.dp),
            )
        }
        if (perm.granted) {
            Text("Allowed", color = SxColors.Green, fontSize = 12.sp, fontWeight = FontWeight.Bold)
        } else {
            Text("Allow", color = SxColors.Bronze, fontSize = 12.sp, fontWeight = FontWeight.Bold)
        }
    }
}

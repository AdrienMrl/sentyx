package com.sentyx.app.feature.pairing

import androidx.compose.foundation.background
import androidx.compose.foundation.border
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
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxDimens
import com.sentyx.app.core.designsystem.SxPrimaryButton

/** Name the device and confirm vehicle details. */
@Composable
fun NameDeviceScreen(
    vm: PairingViewModel,
    onBack: () -> Unit,
    onContinue: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()
    OnboardingScreen {
        OnboardingBack(onBack)
        OnboardingTitle("Name your setup", Modifier.padding(top = 14.dp))
        Column(
            modifier = Modifier.padding(top = 20.dp),
            verticalArrangement = Arrangement.spacedBy(14.dp),
        ) {
            PairEditField(
                label = "Device name",
                value = state.deviceName,
                onValueChange = vm::setDeviceName,
            )
            StaticRow(label = "Vehicle model", value = state.vehicleModel)
            PairEditField(
                label = "Vehicle nickname",
                value = state.nickname,
                onValueChange = vm::setNickname,
                placeholder = "Daily driver",
                optional = true,
            )
            StaticRow(label = "Timezone", value = state.timezone)
        }
        Spacer(Modifier.weight(1f))
        state.configureError?.let { err ->
            Text(
                err,
                color = SxColors.Red,
                fontSize = 13.sp,
                modifier = Modifier.padding(bottom = 10.dp),
            )
        }
        SxPrimaryButton(
            if (state.configuring) "Setting up…" else "Continue",
            onClick = { if (!state.configuring) vm.configure(onSuccess = onContinue) },
        )
    }
}

/** Label + bordered value box with a trailing chevron (non-editable). */
@Composable
private fun StaticRow(label: String, value: String) {
    val shape = RoundedCornerShape(SxDimens.InputRadius)
    Column {
        Text(
            label,
            color = SxColors.Muted,
            fontSize = 12.sp,
            fontWeight = FontWeight.Bold,
            modifier = Modifier.padding(bottom = 6.dp),
        )
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .clip(shape)
                .background(SxColors.Card)
                .border(1.dp, SxColors.Border, shape)
                .padding(horizontal = 14.dp, vertical = 13.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween,
        ) {
            Text(value, color = FieldInk, fontSize = 14.sp, fontWeight = FontWeight.SemiBold)
            Text("›", color = SxColors.Chevron, fontSize = 16.sp)
        }
    }
}

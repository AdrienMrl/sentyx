package com.sentyx.app.feature.device

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SentyxLogo
import com.sentyx.app.core.designsystem.SxCard
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.domain.model.DeviceSnapshot

/** Devices list: the currently paired Pi plus a dashed "pair another" tile. */
@Composable
fun AddDeviceScreen(
    vm: DeviceViewModel,
    onBack: () -> Unit,
    onPairNew: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(top = 56.dp, bottom = 40.dp)
            .padding(horizontal = 22.dp),
    ) {
        com.sentyx.app.core.designsystem.SxBackHeader(
            title = "Your devices",
            onBack = onBack,
            modifier = Modifier.padding(top = 8.dp),
        )

        Column(
            modifier = Modifier.fillMaxWidth().padding(top = 18.dp),
            verticalArrangement = Arrangement.spacedBy(11.dp),
        ) {
            state.snapshot?.let { CurrentDeviceCard(it) }
            PairAnotherTile(onPairNew)
        }
    }
}

@Composable
private fun CurrentDeviceCard(snapshot: DeviceSnapshot) {
    SxCard {
        Row(
            modifier = Modifier.fillMaxWidth(),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            SentyxLogo(size = 36.dp)
            Column(Modifier.weight(1f)) {
                Text(snapshot.name, color = SxColors.Ink, fontSize = 14.sp, fontWeight = FontWeight.Bold)
                Text("${snapshot.vehicleModel} · online", color = SxColors.Muted, fontSize = 12.sp)
            }
            Text("✓", color = SxColors.Green, fontSize = 15.sp, fontWeight = FontWeight.Bold)
        }
    }
}

@Composable
private fun PairAnotherTile(onPairNew: () -> Unit) {
    Box(
        modifier = Modifier
            .fillMaxWidth()
            .drawBehind {
                val stroke = 1.5.dp.toPx()
                drawRoundRect(
                    color = SxColors.Sand,
                    style = Stroke(
                        width = stroke,
                        pathEffect = PathEffect.dashPathEffect(floatArrayOf(9f, 7f)),
                    ),
                    cornerRadius = androidx.compose.ui.geometry.CornerRadius(14.dp.toPx()),
                )
            }
            .clickable(onClick = onPairNew)
            .padding(vertical = 16.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text("+ Pair another Sentyx Pi", color = SxColors.Bronze, fontSize = 14.sp, fontWeight = FontWeight.Bold)
    }
}

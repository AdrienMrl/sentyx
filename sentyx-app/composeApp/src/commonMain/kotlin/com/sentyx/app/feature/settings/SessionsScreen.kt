package com.sentyx.app.feature.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxDimens
import com.sentyx.app.domain.model.SessionInfo

/**
 * Signed-in devices: one card per session. The current session shows a green
 * "Current" label; others show a red "Revoke" action wired to the view model.
 */
@Composable
fun SessionsScreen(
    vm: AccountViewModel,
    onBack: () -> Unit,
) {
    val state by vm.state.collectAsState()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(start = 22.dp, end = 22.dp, top = 56.dp, bottom = 40.dp),
    ) {
        SxBackHeader("Signed-in devices", onBack, modifier = Modifier.padding(top = 8.dp))

        Column(
            modifier = Modifier.padding(top = 18.dp),
            verticalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            state.sessions.forEach { session ->
                SessionCard(session, onRevoke = { vm.revokeSession(session.id) })
            }
        }
    }
}

@Composable
private fun SessionCard(session: SessionInfo, onRevoke: () -> Unit) {
    Row(
        modifier = Modifier
            .clip(RoundedCornerShape(SxDimens.CardRadiusSmall))
            .background(SxColors.Card)
            .border(1.dp, SxColors.Border, RoundedCornerShape(SxDimens.CardRadiusSmall))
            .padding(14.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Text(session.icon, fontSize = 17.sp)
        Column(Modifier.weight(1f)) {
            Text(session.deviceName, color = SxColors.Ink, fontSize = 13.5.sp, fontWeight = FontWeight.Bold)
            Text(session.locationLine, color = SxColors.Muted, fontSize = 11.5.sp)
        }
        if (session.isCurrent) {
            Text("Current", color = SxColors.Green, fontSize = 11.sp, fontWeight = FontWeight.Bold)
        } else {
            Text(
                "Revoke",
                color = SxColors.Red,
                fontSize = 12.sp,
                fontWeight = FontWeight.Bold,
                modifier = Modifier.clickable(onClick = onRevoke),
            )
        }
    }
}

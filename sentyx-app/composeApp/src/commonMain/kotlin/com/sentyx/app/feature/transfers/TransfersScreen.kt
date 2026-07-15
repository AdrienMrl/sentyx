package com.sentyx.app.feature.transfers

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SxCard
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxProgressBar
import com.sentyx.app.core.designsystem.SxSectionHeader
import com.sentyx.app.core.designsystem.SxTextLink
import com.sentyx.app.core.designsystem.StripedThumb
import com.sentyx.app.domain.model.Transfer

/**
 * Transfers tab root: a title with a settings button, the active-and-recent
 * queue of in-flight/failed/canceled transfers, and a nav card into the
 * on-phone download library.
 */
@Composable
fun TransfersScreen(
    vm: TransfersViewModel,
    onOpenLibrary: () -> Unit,
    onOpenSettings: () -> Unit,
) {
    val state by vm.uiState.collectAsStateWithLifecycle()
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .verticalScroll(rememberScrollState())
            .padding(top = 56.dp, bottom = 96.dp)
            .padding(horizontal = 22.dp),
    ) {
        Row(
            modifier = Modifier.fillMaxWidth().padding(top = 8.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween,
        ) {
            Text(
                "Transfers",
                color = SxColors.Ink,
                fontSize = 26.sp,
                fontWeight = FontWeight.ExtraBold,
                style = TextStyle(letterSpacing = (-0.5).sp),
            )
            Box(
                modifier = Modifier
                    .size(34.dp)
                    .clip(CircleShape)
                    .background(SxColors.Card)
                    .border(1.dp, SxColors.Border, CircleShape)
                    .clickable(onClick = onOpenSettings),
                contentAlignment = Alignment.Center,
            ) {
                Text("⚙", fontSize = 15.sp)
            }
        }

        SxSectionHeader("Active & recent")

        if (state.active.isNotEmpty()) {
            Column(verticalArrangement = Arrangement.spacedBy(11.dp)) {
                state.active.forEach { transfer ->
                    ActiveTransferCard(transfer, vm)
                }
            }
        }

        SxCard(
            modifier = Modifier.padding(top = 16.dp),
            onClick = onOpenLibrary,
        ) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                Text("📁", fontSize = 18.sp)
                Column(modifier = Modifier.weight(1f)) {
                    Text("Downloaded clips", color = SxColors.Ink, fontSize = 14.sp, fontWeight = FontWeight.Bold)
                    Text(
                        "On this phone · play, share, export",
                        color = SxColors.Muted,
                        fontSize = 12.sp,
                    )
                }
                Text("›", color = SxColors.Chevron, fontSize = 18.sp)
            }
        }
    }
}

@Composable
private fun ActiveTransferCard(transfer: Transfer, vm: TransfersViewModel) {
    val visual = transfer.visual()
    SxCard {
        Column {
            Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                StripedThumb(
                    modifier = Modifier
                        .size(44.dp)
                        .clip(RoundedCornerShape(11.dp)),
                )
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        transfer.title,
                        color = SxColors.Ink,
                        fontSize = 13.5.sp,
                        fontWeight = FontWeight.Bold,
                        lineHeight = 17.5.sp,
                    )
                    Text(
                        "${transfer.sizeLabel} · ${transfer.route.label}",
                        color = SxColors.Muted,
                        fontSize = 11.5.sp,
                        modifier = Modifier.padding(top = 2.dp),
                    )
                }
                Text(
                    visual.pctLabel,
                    color = visual.statusColor,
                    fontSize = 12.sp,
                    fontWeight = FontWeight.Bold,
                )
            }

            if (visual.showBar) {
                SxProgressBar(
                    pct = visual.pctFraction,
                    color = visual.barColor,
                    modifier = Modifier.padding(top = 11.dp),
                )
            }

            Row(
                modifier = Modifier.fillMaxWidth().padding(top = 10.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.SpaceBetween,
            ) {
                Text(
                    visual.statusLabel,
                    color = visual.statusColor,
                    fontSize = 11.5.sp,
                    fontWeight = FontWeight.SemiBold,
                )
                Row(horizontalArrangement = Arrangement.spacedBy(14.dp)) {
                    queueActions(transfer, vm).forEach { (label, onTap) ->
                        SxTextLink(label, onTap)
                    }
                }
            }
        }
    }
}

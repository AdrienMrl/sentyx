package com.sentyx.app.feature.transfers

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
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
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxCard
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxEmptyState
import com.sentyx.app.core.designsystem.SxTextLink
import com.sentyx.app.core.designsystem.StripedThumb
import com.sentyx.app.domain.model.Transfer

/**
 * On-phone library of completed downloads. Each entry can be played (mock) or
 * removed; an empty library shows a prompt to download an original.
 */
@Composable
fun DownloadLibraryScreen(
    vm: TransfersViewModel,
    onBack: () -> Unit,
) {
    val state by vm.uiState.collectAsStateWithLifecycle()
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .verticalScroll(rememberScrollState())
            .padding(top = 56.dp, bottom = 40.dp)
            .padding(horizontal = 22.dp),
    ) {
        SxBackHeader(title = "Downloaded clips", onBack = onBack, modifier = Modifier.padding(top = 8.dp))

        if (state.library.isEmpty()) {
            SxEmptyState(
                emoji = "📁",
                title = "No downloads yet",
                body = "Download an original from any event to keep it on your phone.",
                modifier = Modifier.fillMaxWidth().padding(top = 44.dp),
            )
        } else {
            Column(
                modifier = Modifier.padding(top = 18.dp),
                verticalArrangement = Arrangement.spacedBy(11.dp),
            ) {
                state.library.forEach { transfer ->
                    LibraryCard(transfer, vm)
                }
            }
        }
    }
}

@Composable
private fun LibraryCard(transfer: Transfer, vm: TransfersViewModel) {
    SxCard {
        Row(
            modifier = Modifier.fillMaxWidth(),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            StripedThumb(
                modifier = Modifier
                    .size(56.dp)
                    .clip(RoundedCornerShape(11.dp)),
                overlay = { Text("▶", color = SxColors.Muted, fontSize = 16.sp) },
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
            Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                SxTextLink("Play", onClick = vm::play)
                SxTextLink("Remove", onClick = { vm.remove(transfer.id) })
            }
        }
    }
}

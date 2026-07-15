package com.sentyx.app.feature.notifications

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SeverityDot
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxCard
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.domain.model.NotificationEntry

/**
 * Past notifications. Entries with a non-null event id are tappable and open
 * the referenced event via [onOpenEvent].
 */
@Composable
fun NotificationHistoryScreen(
    vm: NotificationsViewModel,
    onBack: () -> Unit,
    onOpenEvent: (String) -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(top = 56.dp, bottom = 40.dp)
            .padding(horizontal = 22.dp),
    ) {
        SxBackHeader(title = "History", onBack = onBack, modifier = Modifier.padding(top = 8.dp))

        Column(
            modifier = Modifier.padding(top = 18.dp),
            verticalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            state.history.forEach { entry ->
                HistoryCard(
                    entry = entry,
                    onClick = entry.eventId?.let { id -> { onOpenEvent(id) } },
                )
            }
        }
    }
}

@Composable
private fun HistoryCard(entry: NotificationEntry, onClick: (() -> Unit)?) {
    SxCard(onClick = onClick) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            SeverityDot(severity = entry.severity, size = 9.dp)
            Column(Modifier.weight(1f)) {
                Text(entry.title, color = SxColors.Ink, fontSize = 13.5.sp, fontWeight = FontWeight.Bold)
                Text(
                    entry.subtitle,
                    color = SxColors.Muted,
                    fontSize = 11.5.sp,
                    modifier = Modifier.padding(top = 1.dp),
                )
            }
            Text("›", color = SxColors.Chevron, fontSize = 16.sp)
        }
    }
}

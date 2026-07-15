package com.sentyx.app.feature.notifications

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxListGroup
import com.sentyx.app.core.designsystem.SxListRow
import com.sentyx.app.core.designsystem.SxToggle
import com.sentyx.app.core.designsystem.SxToggleRow

/**
 * Quiet-hours settings: on/off toggle, static start/end/days values, and a
 * separate "always allow urgent" card.
 */
@Composable
fun QuietHoursScreen(
    vm: NotificationsViewModel,
    onBack: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()
    val quiet = state.quietHours

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(top = 56.dp, bottom = 40.dp)
            .padding(horizontal = 22.dp),
    ) {
        SxBackHeader(title = "Quiet hours", onBack = onBack, modifier = Modifier.padding(top = 8.dp))

        SxListGroup(modifier = Modifier.padding(top = 18.dp)) {
            SxListRow(
                label = "Quiet hours on",
                trailing = {
                    SxToggle(checked = quiet.enabled, onToggle = { vm.setQuietHoursEnabled(it) })
                },
            )
            SxListRow(label = "Start", value = quiet.start)
            SxListRow(label = "End", value = quiet.end)
            SxListRow(label = "Days", value = quiet.days, showDivider = false)
        }

        SxListGroup(modifier = Modifier.padding(top = 12.dp)) {
            SxToggleRow(
                title = "Always allow urgent",
                subtitle = "Urgent events alert even during quiet hours",
                checked = quiet.allowUrgent,
                onToggle = { vm.setAllowUrgentDuringQuietHours(it) },
            )
        }
    }
}

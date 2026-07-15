package com.sentyx.app.feature.settings

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxListGroup
import com.sentyx.app.core.designsystem.SxListRow
import com.sentyx.app.core.designsystem.SxToggle

/**
 * Security screen: change password, a two-factor toggle, and a shortcut to the
 * signed-in devices list. Shares [AccountViewModel] with the account screen.
 */
@Composable
fun SecurityScreen(
    vm: AccountViewModel,
    onBack: () -> Unit,
    onOpenSessions: () -> Unit,
) {
    val state by vm.state.collectAsState()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(start = 22.dp, end = 22.dp, top = 56.dp, bottom = 40.dp),
    ) {
        SxBackHeader("Security", onBack, modifier = Modifier.padding(top = 8.dp))

        SxListGroup(modifier = Modifier.padding(top = 18.dp)) {
            SxListRow("Change password", showChevron = true, onClick = vm::changePassword)
            SxListRow(
                "Two-factor auth",
                trailing = {
                    SxToggle(
                        checked = state.twoFactorEnabled,
                        onToggle = vm::setTwoFactorEnabled,
                    )
                },
            )
            SxListRow(
                "Signed-in devices",
                showChevron = true,
                showDivider = false,
                onClick = onOpenSessions,
            )
        }
    }
}

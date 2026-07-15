package com.sentyx.app.feature.settings

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxListGroup
import com.sentyx.app.core.designsystem.SxListRow
import com.sentyx.app.domain.model.PermissionState

/**
 * Permission status: one row per phone permission, its state colored green
 * (Allowed) / red (Denied · enable) / muted (Ask). Denied rows are tappable and
 * surface a mock "enable" toast.
 */
@Composable
fun PermissionStatusScreen(
    vm: PermissionStatusViewModel,
    onBack: () -> Unit,
) {
    val permissions by vm.permissions.collectAsState()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(start = 22.dp, end = 22.dp, top = 56.dp, bottom = 40.dp),
    ) {
        SxBackHeader("App permissions", onBack, modifier = Modifier.padding(top = 8.dp))

        SxListGroup(modifier = Modifier.padding(top = 18.dp)) {
            permissions.forEachIndexed { index, permission ->
                val last = index == permissions.lastIndex
                when (permission.state) {
                    PermissionState.Allowed -> SxListRow(
                        permission.name,
                        showDivider = !last,
                        trailing = { StatusText("Allowed", SxColors.Green) },
                    )
                    PermissionState.Denied -> SxListRow(
                        permission.name,
                        showDivider = !last,
                        onClick = vm::enable,
                        trailing = { StatusText("Denied · enable ›", SxColors.Red) },
                    )
                    PermissionState.Ask -> SxListRow(
                        permission.name,
                        showDivider = !last,
                        trailing = { StatusText("Ask", SxColors.Muted) },
                    )
                }
            }
        }
    }
}

@Composable
private fun StatusText(text: String, color: Color) {
    Text(text, color = color, fontSize = 12.5.sp, fontWeight = FontWeight.Bold)
}

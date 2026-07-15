package com.sentyx.app.feature.device

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxListGroup
import com.sentyx.app.core.designsystem.SxListRow
import com.sentyx.app.core.designsystem.SxPrimaryButton
import com.sentyx.app.core.designsystem.SxSecondaryButton
import com.sentyx.app.domain.model.DiagnosticEntry

/** Read-only diagnostics table + connection-test / export actions. */
@Composable
fun DiagnosticsScreen(
    vm: DeviceViewModel,
    onBack: () -> Unit,
    onRunTest: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(top = 56.dp, bottom = 40.dp)
            .padding(horizontal = 22.dp),
    ) {
        SxBackHeader(title = "Diagnostics", onBack = onBack, modifier = Modifier.padding(top = 8.dp))

        Box(Modifier.padding(top = 18.dp)) {
            SxListGroup {
                state.diagnostics.forEachIndexed { index, entry ->
                    SxListRow(
                        label = entry.label,
                        labelColor = SxColors.Muted,
                        value = entry.value,
                        valueColor = diagnosticValueColor(entry),
                        showDivider = index < state.diagnostics.lastIndex,
                    )
                }
            }
        }

        Box(Modifier.padding(top = 14.dp)) {
            SxSecondaryButton(text = "Run connection test", onClick = onRunTest)
        }
        Box(Modifier.padding(top = 10.dp)) {
            SxPrimaryButton(text = "Export diagnostic report", onClick = { vm.comingSoon("Export diagnostic report") })
        }
    }
}

/**
 * Green for a healthy word-status (e.g. "Active"), red for an unhealthy entry,
 * and default ink for numeric metrics (latency, counts, uptime).
 */
private fun diagnosticValueColor(entry: DiagnosticEntry): Color = when {
    !entry.healthy -> SxColors.Red
    entry.value.any(Char::isDigit) -> SxColors.Ink
    else -> SxColors.Green
}

package com.sentyx.app.feature.notifications

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
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
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxDimens
import com.sentyx.app.core.designsystem.SxListGroup
import com.sentyx.app.core.designsystem.SxListRow
import com.sentyx.app.core.designsystem.SxSectionHeader
import com.sentyx.app.core.designsystem.SxToggleRow
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.domain.model.NotificationRule
import com.sentyx.app.domain.model.Severity

/** Order of the "alert me for at least" chips and rule sections. */
private val MIN_SEVERITY_ORDER = listOf(Severity.Urgent, Severity.Attention, Severity.Routine)
private val RULE_SECTIONS = listOf("Events", "Types", "System")

/**
 * Notifications root: minimum-severity chips, per-section rule toggles, and a
 * "MORE" group linking to quiet hours / history plus the hide-preview toggle.
 */
@Composable
fun AlertsScreen(
    vm: NotificationsViewModel,
    onBack: () -> Unit,
    onOpenQuietHours: () -> Unit,
    onOpenHistory: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(top = 56.dp, bottom = 40.dp)
            .padding(horizontal = 22.dp),
    ) {
        SxBackHeader(title = "Notifications", onBack = onBack, modifier = Modifier.padding(top = 8.dp))

        SxSectionHeader("Alert me for at least")
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.fillMaxWidth()) {
            MIN_SEVERITY_ORDER.forEach { severity ->
                MinSeverityChip(
                    label = severityLabelText(severity),
                    selected = state.minSeverity == severity,
                    onClick = { vm.setMinSeverity(severity) },
                    modifier = Modifier.weight(1f),
                )
            }
        }

        RULE_SECTIONS.forEach { section ->
            val rows = state.rules.filter { it.section == section }
            if (rows.isNotEmpty()) {
                SxSectionHeader(section)
                SxListGroup {
                    rows.forEachIndexed { index, rule ->
                        RuleRow(
                            rule = rule,
                            onToggle = { vm.setRuleEnabled(rule.key, it) },
                        )
                        if (index != rows.lastIndex) RowDivider()
                    }
                }
            }
        }

        SxSectionHeader("More")
        SxListGroup {
            SxListRow(
                label = "Quiet hours",
                value = quietHoursSummary(state.quietHours.start, state.quietHours.end),
                onClick = onOpenQuietHours,
                showChevron = true,
            )
            SxListRow(
                label = "Notification history",
                onClick = onOpenHistory,
                showChevron = true,
            )
            SxListRow(
                label = "Hide preview content",
                value = if (state.hidePreviewContent) "On" else "Off",
                onClick = { vm.setHidePreviewContent(!state.hidePreviewContent) },
                showChevron = true,
                showDivider = false,
            )
        }
    }
}

@Composable
private fun MinSeverityChip(
    label: String,
    selected: Boolean,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val shape = RoundedCornerShape(11.dp)
    Box(
        modifier = modifier
            .clip(shape)
            .background(if (selected) SxColors.Ink else SxColors.Card)
            .border(1.dp, if (selected) SxColors.Ink else SxColors.Border, shape)
            .clickable(onClick = onClick)
            .padding(vertical = 10.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = label,
            color = if (selected) SxColors.OnInk else SxColors.InkSecondary,
            fontSize = 12.5.sp,
            fontWeight = FontWeight.Bold,
            textAlign = TextAlign.Center,
        )
    }
}

@Composable
private fun RuleRow(rule: NotificationRule, onToggle: (Boolean) -> Unit) {
    SxToggleRow(
        title = rule.title,
        checked = rule.enabled,
        onToggle = onToggle,
        subtitle = rule.subtitle.ifBlank { null },
    )
}

@Composable
private fun RowDivider() {
    Box(
        Modifier
            .fillMaxWidth()
            .height(1.dp)
            .background(SxColors.Divider),
    )
}

/** "Urgent" / "Attention" / "Routine". */
private fun severityLabelText(severity: Severity): String = when (severity) {
    Severity.Urgent -> "Urgent"
    Severity.Attention -> "Attention"
    Severity.Routine -> "Routine"
}

/** Compact quiet-hours window label, e.g. "10 PM–7 AM" from "10:00 PM"/"7:00 AM". */
private fun quietHoursSummary(start: String, end: String): String =
    "${compactTime(start)}–${compactTime(end)}"

private fun compactTime(time: String): String = time.replace(":00", "")

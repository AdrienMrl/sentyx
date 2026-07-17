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
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxListGroup
import com.sentyx.app.core.designsystem.SxListRow
import com.sentyx.app.core.designsystem.SxSectionHeader
import com.sentyx.app.core.designsystem.SxToggleRow
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.permissions.LocalNotificationPermissionController
import com.sentyx.app.domain.model.MinThreatLevel
import com.sentyx.app.domain.model.NotificationRule

/** Order of the threat-level threshold options (least → most notifications). */
private val THREAT_LEVEL_ORDER = listOf(
    MinThreatLevel.Off,
    MinThreatLevel.HighOnly,
    MinThreatLevel.MediumAndUp,
    MinThreatLevel.LowAndUp,
    MinThreatLevel.Everything,
)
private val RULE_SECTIONS = listOf("Events", "Types", "System")

/**
 * Notifications root: the single-choice push-threat threshold, per-section rule
 * toggles, and a "MORE" group linking to quiet hours / history plus the
 * hide-preview toggle. Requests the Android 13+ notification permission on open.
 */
@Composable
fun AlertsScreen(
    vm: NotificationsViewModel,
    onBack: () -> Unit,
    onOpenQuietHours: () -> Unit,
    onOpenHistory: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()

    // Prompt for POST_NOTIFICATIONS the first time the user opens Notifications
    // settings (no-op below Android 13 and on iOS). Chosen over prompting at
    // sign-in so the ask has clear context.
    val notificationPermission = LocalNotificationPermissionController.current
    LaunchedEffect(Unit) { notificationPermission.ensurePermission() }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(top = 56.dp, bottom = 40.dp)
            .padding(horizontal = 22.dp),
    ) {
        SxBackHeader(title = "Notifications", onBack = onBack, modifier = Modifier.padding(top = 8.dp))

        SxSectionHeader("Alert me for")
        SxListGroup {
            THREAT_LEVEL_ORDER.forEachIndexed { index, level ->
                ThreatLevelRow(
                    level = level,
                    selected = state.minThreatLevel == level,
                    onClick = { vm.setMinThreatLevel(level) },
                    showDivider = index != THREAT_LEVEL_ORDER.lastIndex,
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

/** One selectable threshold option: label + detail on the left, a check dot when selected. */
@Composable
private fun ThreatLevelRow(
    level: MinThreatLevel,
    selected: Boolean,
    onClick: () -> Unit,
    showDivider: Boolean,
) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onClick),
    ) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 14.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Column(Modifier.weight(1f)) {
                Text(
                    level.label,
                    color = SxColors.Ink,
                    fontSize = 13.5.sp,
                    fontWeight = FontWeight.SemiBold,
                )
                Text(
                    level.detail,
                    color = SxColors.Muted,
                    fontSize = 11.5.sp,
                    modifier = Modifier.padding(top = 1.dp),
                )
            }
            SelectDot(selected = selected)
        }
        if (showDivider) RowDivider()
    }
}

/** Filled ink circle with a cream check when selected; hollow ring otherwise. */
@Composable
private fun SelectDot(selected: Boolean) {
    Box(
        modifier = Modifier
            .size(20.dp)
            .clip(CircleShape)
            .background(if (selected) SxColors.Ink else SxColors.Card)
            .border(1.dp, if (selected) SxColors.Ink else SxColors.Border, CircleShape),
        contentAlignment = Alignment.Center,
    ) {
        if (selected) {
            Text("✓", color = SxColors.OnInk, fontSize = 12.sp, fontWeight = FontWeight.Bold)
        }
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

/** Compact quiet-hours window label, e.g. "10 PM–7 AM" from "10:00 PM"/"7:00 AM". */
private fun quietHoursSummary(start: String, end: String): String =
    "${compactTime(start)}–${compactTime(end)}"

private fun compactTime(time: String): String = time.replace(":00", "")

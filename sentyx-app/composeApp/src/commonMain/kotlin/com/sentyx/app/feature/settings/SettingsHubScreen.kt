package com.sentyx.app.feature.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxListGroup
import com.sentyx.app.core.designsystem.SxSectionHeader

/**
 * Settings tab root. Three grouped lists (Sentyx / Preferences / Account),
 * a red centered "Sign out", and the mock version footer. Every row navigates
 * through a callback; the hub itself only supplies subtitles and the sign-out.
 */
@Composable
fun SettingsHubScreen(
    vm: SettingsHubViewModel,
    onOpenDevice: () -> Unit,
    onOpenDeviceSettings: () -> Unit,
    onOpenTransferSettings: () -> Unit,
    onOpenAlerts: () -> Unit,
    onOpenSubscription: () -> Unit,
    onOpenAppSettings: () -> Unit,
    onOpenAccount: () -> Unit,
    onOpenPrivacy: () -> Unit,
    onOpenPrototypeStates: () -> Unit,
    onSignOut: () -> Unit,
) {
    val state by vm.state.collectAsState()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(start = 22.dp, end = 22.dp, top = 56.dp, bottom = 96.dp),
    ) {
        Text(
            "Settings",
            color = SxColors.Ink,
            fontSize = 26.sp,
            fontWeight = FontWeight.ExtraBold,
            style = TextStyle(letterSpacing = (-0.5).sp),
            modifier = Modifier.padding(top = 8.dp),
        )

        SxSectionHeader("Sentyx")
        SxListGroup {
            HubRow("⬡", "Device — Garage Pi", null, onOpenDevice, showDivider = true)
            HubRow("⚙", "Device settings & maintenance", null, onOpenDeviceSettings, showDivider = true)
            HubRow("↓", "Transfers & downloads", "${state.activeTransfers} active", onOpenTransferSettings, showDivider = false)
        }

        SxSectionHeader("Preferences")
        SxListGroup {
            HubRow("🔔", "Notifications", "Alerts, quiet hours", onOpenAlerts, showDivider = true)
            HubRow("★", "Subscription", state.planName, onOpenSubscription, showDivider = true)
            HubRow("🌐", "App settings", "Units, language", onOpenAppSettings, showDivider = false)
        }

        SxSectionHeader("Account")
        SxListGroup {
            HubRow("👤", "Account & profile", state.email, onOpenAccount, showDivider = true)
            HubRow("🔒", "Privacy & data", null, onOpenPrivacy, showDivider = true)
            HubRow("🛟", "Help & support", null, vm::helpAndSupport, showDivider = true)
            HubRow("🧪", "Prototype states", "Toggle demo conditions", onOpenPrototypeStates, showDivider = false)
        }

        Text(
            "Sign out",
            color = SxColors.Red,
            fontSize = 14.sp,
            fontWeight = FontWeight.Bold,
            textAlign = TextAlign.Center,
            modifier = Modifier
                .fillMaxWidth()
                .padding(top = 22.dp)
                .clickable { vm.signOut(onSignOut) },
        )
        Text(
            "Sentyx · v1.0.0 (mock)",
            color = SxColors.Hint,
            fontSize = 11.sp,
            textAlign = TextAlign.Center,
            modifier = Modifier
                .fillMaxWidth()
                .padding(top = 12.dp),
        )
    }
}

/** One hub row: leading emoji icon, label + optional subtitle, trailing chevron. */
@Composable
private fun HubRow(
    icon: String,
    label: String,
    subtitle: String?,
    onClick: () -> Unit,
    showDivider: Boolean,
) {
    Column(Modifier.fillMaxWidth().clickable(onClick = onClick)) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 16.dp, vertical = 14.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text(
                icon,
                fontSize = 16.sp,
                textAlign = TextAlign.Center,
                modifier = Modifier.width(22.dp),
            )
            Column(Modifier.weight(1f)) {
                Text(label, color = SxColors.Ink, fontSize = 14.sp, fontWeight = FontWeight.SemiBold)
                if (!subtitle.isNullOrEmpty()) {
                    Text(
                        subtitle,
                        color = SxColors.Muted,
                        fontSize = 11.5.sp,
                        modifier = Modifier.padding(top = 1.dp),
                    )
                }
            }
            Text("›", color = SxColors.Chevron, fontSize = 16.sp)
        }
        if (showDivider) {
            Box(
                Modifier
                    .fillMaxWidth()
                    .height(1.dp)
                    .background(SxColors.Divider),
            )
        }
    }
}

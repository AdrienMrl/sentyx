package com.sentyx.app.feature.settings

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxListGroup
import com.sentyx.app.core.designsystem.SxListRow
import com.sentyx.app.core.designsystem.SxSectionHeader

/**
 * Privacy & data screen. A permissions nav card leads to the permission-status
 * screen; a Data group and a red "Delete account" action are all mock toasts.
 * Uses [PrivacyViewModel] (toast-only) for the stub rows.
 */
@Composable
fun PrivacyScreen(
    vm: PrivacyViewModel,
    onBack: () -> Unit,
    onOpenPermissionStatus: () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(start = 22.dp, end = 22.dp, top = 56.dp, bottom = 40.dp),
    ) {
        SxBackHeader("Privacy & data", onBack, modifier = Modifier.padding(top = 8.dp))

        SxSectionHeader("App permissions")
        SxListGroup {
            SxListRow(
                "Bluetooth, network, notifications…",
                showChevron = true,
                showDivider = false,
                onClick = onOpenPermissionStatus,
            )
        }

        SxSectionHeader("Data")
        SxListGroup {
            SxListRow("Data retention", value = "30 days", showChevron = true, onClick = vm::stub)
            SxListRow("Export account data", showChevron = true, onClick = vm::stub)
            SxListRow(
                "Delete cloud event data",
                showChevron = true,
                showDivider = false,
                labelColor = SxColors.Red,
                onClick = vm::stub,
            )
        }

        Text(
            "Delete account",
            color = SxColors.Red,
            fontSize = 14.sp,
            fontWeight = FontWeight.Bold,
            textAlign = TextAlign.Center,
            modifier = Modifier
                .fillMaxWidth()
                .padding(top = 20.dp)
                .clickable(onClick = vm::stub),
        )
    }
}

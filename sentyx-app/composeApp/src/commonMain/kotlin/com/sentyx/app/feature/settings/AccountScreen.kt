package com.sentyx.app.feature.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxDimens
import com.sentyx.app.core.designsystem.SxListGroup
import com.sentyx.app.core.designsystem.SxListRow
import com.sentyx.app.core.designsystem.SxSectionHeader

/**
 * Account screen: identity card, a Profile group (name/email, security,
 * sessions) and a Billing group (subscription, restore purchases).
 */
@Composable
fun AccountScreen(
    vm: AccountViewModel,
    onBack: () -> Unit,
    onOpenSecurity: () -> Unit,
    onOpenSessions: () -> Unit,
    onOpenSubscription: () -> Unit,
) {
    val state by vm.state.collectAsState()
    val profile = state.profile

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(start = 22.dp, end = 22.dp, top = 56.dp, bottom = 40.dp),
    ) {
        SxBackHeader("Account", onBack, modifier = Modifier.padding(top = 8.dp))

        Row(
            modifier = Modifier
                .padding(top = 18.dp)
                .clip(RoundedCornerShape(SxDimens.CardRadius))
                .background(SxColors.Card)
                .border(1.dp, SxColors.Border, RoundedCornerShape(SxDimens.CardRadius))
                .padding(16.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(14.dp),
        ) {
            Box(
                modifier = Modifier
                    .size(52.dp)
                    .clip(CircleShape)
                    .background(SxColors.Ink),
                contentAlignment = Alignment.Center,
            ) {
                Text(
                    profile?.initials ?: "AR",
                    color = SxColors.Gold,
                    fontSize = 20.sp,
                    fontWeight = FontWeight.ExtraBold,
                )
            }
            Column {
                Text(
                    profile?.name ?: "Alex Rivera",
                    color = SxColors.Ink,
                    fontSize = 16.sp,
                    fontWeight = FontWeight.ExtraBold,
                )
                Text(
                    profile?.email ?: "alex@example.com",
                    color = SxColors.Muted,
                    fontSize = 12.5.sp,
                )
            }
        }

        SxSectionHeader("Profile")
        SxListGroup {
            SxListRow("Name & email", showChevron = true, onClick = vm::editNameEmail)
            SxListRow("Password & security", showChevron = true, onClick = onOpenSecurity)
            SxListRow(
                "Signed-in devices",
                value = state.sessions.size.toString(),
                showChevron = true,
                showDivider = false,
                onClick = onOpenSessions,
            )
        }

        SxSectionHeader("Billing")
        SxListGroup {
            SxListRow(
                "Subscription",
                value = state.planName,
                showChevron = true,
                onClick = onOpenSubscription,
            )
            SxListRow(
                "Restore purchases",
                showChevron = true,
                showDivider = false,
                onClick = vm::restorePurchases,
            )
        }
    }
}

package com.sentyx.app.feature.settings

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
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxDimens
import com.sentyx.app.core.designsystem.SxBottomSheet
import com.sentyx.app.core.designsystem.SxPrimaryButton
import com.sentyx.app.core.designsystem.SxSectionHeader
import com.sentyx.app.domain.model.PlanTier

private val FeatureInk = Color(0xFF4A4436)

/**
 * Subscription screen: a dark current-plan card (with a usage bar on Free), the
 * tier list, and the upgrade bottom sheet (Review → Success | Declined).
 */
@Composable
fun SubscriptionScreen(
    vm: SubscriptionViewModel,
    onBack: () -> Unit,
) {
    val state by vm.state.collectAsState()
    val upgrade by vm.upgrade.collectAsState()
    val currentTier = state.tiers.first { it.plan == state.currentPlan }

    Box(Modifier.fillMaxSize()) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .verticalScroll(rememberScrollState())
                .padding(start = 22.dp, end = 22.dp, top = 56.dp, bottom = 40.dp),
        ) {
            SxBackHeader("Subscription", onBack, modifier = Modifier.padding(top = 8.dp))

            // Dark current-plan card.
            Column(
                modifier = Modifier
                    .padding(top = 18.dp)
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(18.dp))
                    .background(SxColors.Ink)
                    .padding(18.dp),
            ) {
                Text(
                    "CURRENT PLAN",
                    color = SxColors.Gold,
                    fontSize = 12.sp,
                    fontWeight = FontWeight.Bold,
                    style = TextStyle(letterSpacing = 1.sp),
                )
                Text(
                    currentTier.name,
                    color = SxColors.OnInk,
                    fontSize = 24.sp,
                    fontWeight = FontWeight.ExtraBold,
                    modifier = Modifier.padding(top = 6.dp),
                )
                state.usage.usageLine?.let { line ->
                    Text(
                        line,
                        color = Color(0xFFC9C2B4),
                        fontSize = 12.5.sp,
                        modifier = Modifier.padding(top = 6.dp),
                    )
                }
                state.usage.usagePct?.let { pct ->
                    Box(
                        Modifier
                            .padding(top = 10.dp)
                            .fillMaxWidth()
                            .height(6.dp)
                            .clip(RoundedCornerShape(3.dp))
                            .background(Color(0x26FFFFFF)),
                    ) {
                        Box(
                            Modifier
                                .fillMaxWidth(pct.coerceIn(0, 100) / 100f)
                                .height(6.dp)
                                .clip(RoundedCornerShape(3.dp))
                                .background(SxColors.Gold),
                        )
                    }
                }
            }

            SxSectionHeader("Plans")
            Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                state.tiers.forEach { tier ->
                    TierCard(
                        tier = tier,
                        isCurrent = tier.plan == state.currentPlan,
                        onChoose = { vm.openUpgrade(tier) },
                        onManage = vm::managePlan,
                    )
                }
            }

            Text(
                "Restore purchases",
                color = SxColors.Bronze,
                fontSize = 13.sp,
                fontWeight = FontWeight.Bold,
                textAlign = TextAlign.Center,
                modifier = Modifier
                    .padding(top = 16.dp)
                    .fillMaxWidth()
                    .clickable(onClick = vm::restorePurchases),
            )
        }

        upgrade?.let { step ->
            SxBottomSheet(onDismiss = vm::close) {
                when (step) {
                    is UpgradeStep.Review -> ReviewContent(step.tier, vm)
                    is UpgradeStep.Success -> ResultContent(
                        circleBg = SxColors.Green,
                        circleFg = SxColors.White,
                        glyph = "✓",
                        title = "You're on ${step.tier.name}",
                        titleColor = SxColors.Ink,
                        body = "Your trial has started. Enjoy unlimited AI analysis and remote full-quality access.",
                        buttonText = "Done",
                        onButton = vm::close,
                    )
                    UpgradeStep.Declined -> ResultContent(
                        circleBg = SxColors.UrgentBg,
                        circleFg = SxColors.RedDeep,
                        glyph = "!",
                        title = "Payment declined",
                        titleColor = SxColors.RedDeep,
                        body = "We couldn't process your payment method. Try another card or come back later.",
                        buttonText = "Close",
                        onButton = vm::close,
                    )
                }
            }
        }
    }
}

@Composable
private fun TierCard(
    tier: PlanTier,
    isCurrent: Boolean,
    onChoose: () -> Unit,
    onManage: () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(18.dp))
            .background(SxColors.Card)
            .border(1.5.dp, SxColors.Border, RoundedCornerShape(18.dp))
            .padding(16.dp),
    ) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.Bottom,
        ) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(tier.name, color = SxColors.Ink, fontSize = 17.sp, fontWeight = FontWeight.ExtraBold)
                if (isCurrent) {
                    Box(
                        Modifier
                            .clip(RoundedCornerShape(999.dp))
                            .background(SxColors.Green)
                            .padding(horizontal = 8.dp, vertical = 2.dp),
                    ) {
                        Text("Current", color = SxColors.White, fontSize = 10.sp, fontWeight = FontWeight.Bold)
                    }
                }
            }
            Row(verticalAlignment = Alignment.Bottom) {
                Text(tier.price, color = SxColors.Ink, fontSize = 20.sp, fontWeight = FontWeight.ExtraBold)
                if (tier.period.isNotEmpty()) {
                    Text(tier.period, color = SxColors.Muted, fontSize = 12.sp)
                }
            }
        }

        Column(
            modifier = Modifier.padding(top = 12.dp),
            verticalArrangement = Arrangement.spacedBy(6.dp),
        ) {
            tier.features.forEach { FeatureRow(it) }
        }

        if (isCurrent) {
            Box(
                Modifier
                    .padding(top = 14.dp)
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(12.dp))
                    .border(1.dp, SxColors.Sand, RoundedCornerShape(12.dp))
                    .clickable(onClick = onManage)
                    .padding(vertical = 12.dp),
                contentAlignment = Alignment.Center,
            ) {
                Text("Manage plan", color = SxColors.Muted, fontSize = 13.sp, fontWeight = FontWeight.Bold)
            }
        } else {
            Box(
                Modifier
                    .padding(top = 14.dp)
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(12.dp))
                    .background(SxColors.Ink)
                    .clickable(onClick = onChoose)
                    .padding(vertical = 13.dp),
                contentAlignment = Alignment.Center,
            ) {
                Text("Choose ${tier.name}", color = SxColors.OnInk, fontSize = 13.5.sp, fontWeight = FontWeight.Bold)
            }
        }
    }
}

@Composable
private fun FeatureRow(text: String) {
    Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.Top) {
        Text("✓", color = SxColors.Bronze, fontSize = 11.sp)
        Text(text, color = FeatureInk, fontSize = 12.5.sp)
    }
}

@Composable
private fun ReviewContent(tier: PlanTier, vm: SubscriptionViewModel) {
    Column(Modifier.fillMaxWidth()) {
        Text("Upgrade to ${tier.name}", color = SxColors.Ink, fontSize = 20.sp, fontWeight = FontWeight.ExtraBold)
        Text(
            "${tier.price}${tier.period} · 7-day free trial, cancel anytime. Billed after trial.",
            color = SxColors.InkSecondary,
            fontSize = 13.sp,
            lineHeight = 19.5.sp,
            modifier = Modifier.padding(top = 8.dp),
        )
        Column(
            modifier = Modifier
                .padding(top = 14.dp)
                .fillMaxWidth()
                .clip(RoundedCornerShape(SxDimens.CardRadiusSmall))
                .background(SxColors.Card)
                .border(1.dp, SxColors.Border, RoundedCornerShape(SxDimens.CardRadiusSmall))
                .padding(horizontal = 14.dp, vertical = 12.dp),
            verticalArrangement = Arrangement.spacedBy(7.dp),
        ) {
            tier.features.forEach { FeatureRow(it) }
        }
        Column(
            modifier = Modifier.padding(top = 16.dp).fillMaxWidth(),
            verticalArrangement = Arrangement.spacedBy(9.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            SxPrimaryButton("Start free trial", onClick = vm::confirm)
            Text(
                "Simulate declined payment",
                color = SxColors.Hint,
                fontSize = 12.sp,
                modifier = Modifier.clickable(onClick = vm::simulateDeclined),
            )
            Text(
                "Not now",
                color = SxColors.Muted,
                fontSize = 13.sp,
                fontWeight = FontWeight.Bold,
                modifier = Modifier.clickable(onClick = vm::close),
            )
        }
    }
}

@Composable
private fun ResultContent(
    circleBg: Color,
    circleFg: Color,
    glyph: String,
    title: String,
    titleColor: Color,
    body: String,
    buttonText: String,
    onButton: () -> Unit,
) {
    Column(
        modifier = Modifier.fillMaxWidth().padding(vertical = 10.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        Box(
            Modifier.size(64.dp).clip(CircleShape).background(circleBg),
            contentAlignment = Alignment.Center,
        ) {
            Text(glyph, color = circleFg, fontSize = 30.sp)
        }
        Text(title, color = titleColor, fontSize = 20.sp, fontWeight = FontWeight.ExtraBold)
        Text(
            body,
            color = SxColors.InkSecondary,
            fontSize = 13.sp,
            lineHeight = 19.5.sp,
            textAlign = TextAlign.Center,
        )
        SxPrimaryButton(buttonText, onClick = onButton)
    }
}

package com.sentyx.app.data.demo

import com.sentyx.app.domain.model.Plan
import com.sentyx.app.domain.model.PlanTier
import com.sentyx.app.domain.model.SubscriptionUsage
import com.sentyx.app.domain.model.UpgradeResult
import com.sentyx.app.domain.repository.SubscriptionRepository
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.stateIn

/**
 * Demo [SubscriptionRepository]. Usage reflects [DemoStateController.plan];
 * a successful upgrade flips that plan. Payment declines are simulated on demand.
 */
class DemoSubscriptionRepository(
    private val scope: CoroutineScope,
    private val demoState: DemoStateController,
) : SubscriptionRepository {

    override val usage: StateFlow<SubscriptionUsage> =
        demoState.plan
            .map { usageFor(it) }
            .stateIn(scope, SharingStarted.Eagerly, usageFor(demoState.plan.value))

    override val tiers: List<PlanTier> = listOf(
        PlanTier(
            plan = Plan.Free,
            name = "Free",
            price = "$0",
            period = "",
            features = listOf(
                "48-hour event history",
                "10 AI analyses / day",
                "1 vehicle",
                "Local + basic alerts",
            ),
        ),
        PlanTier(
            plan = Plan.Premium,
            name = "Premium",
            price = "$7",
            period = "/mo",
            features = listOf(
                "30-day event history",
                "Unlimited AI analysis",
                "2 vehicles",
                "Advanced notification rules",
                "Remote full-quality access",
            ),
        ),
        PlanTier(
            plan = Plan.Fleet,
            name = "Fleet",
            price = "$19",
            period = "/mo",
            features = listOf(
                "1-year history",
                "Unlimited AI analysis",
                "10 vehicles",
                "Priority processing",
                "Team access & export",
            ),
        ),
    )

    override suspend fun upgrade(plan: Plan, simulateDecline: Boolean): UpgradeResult {
        delay(900)
        if (simulateDecline) return UpgradeResult.PaymentDeclined
        demoState.plan.value = plan
        return UpgradeResult.Success(plan)
    }

    override suspend fun restorePurchases() {
        delay(600)
    }

    private fun usageFor(plan: Plan): SubscriptionUsage = when (plan) {
        Plan.Free -> SubscriptionUsage(
            plan = plan,
            usageLine = "Used 6 of 10 AI analyses today · resets 12:00 AM",
            usagePct = 60,
        )
        else -> SubscriptionUsage(plan = plan, usageLine = null, usagePct = null)
    }
}

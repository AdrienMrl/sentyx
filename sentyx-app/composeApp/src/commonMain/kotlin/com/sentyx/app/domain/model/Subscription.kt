package com.sentyx.app.domain.model

enum class Plan { Free, Premium, Fleet }

/** A purchasable tier as shown on the subscription screen. */
data class PlanTier(
    val plan: Plan,
    val name: String,
    val price: String,
    /** Billing period suffix, e.g. "/mo"; empty for free. */
    val period: String,
    val features: List<String>,
)

/** Current subscription usage shown on the plan card. */
data class SubscriptionUsage(
    val plan: Plan,
    /** e.g. "Used 6 of 10 AI analyses today · resets 12:00 AM"; null when unlimited. */
    val usageLine: String?,
    /** 0..100 for the usage bar; null when unlimited. */
    val usagePct: Int?,
)

sealed interface UpgradeResult {
    data class Success(val plan: Plan) : UpgradeResult
    data object PaymentDeclined : UpgradeResult
}

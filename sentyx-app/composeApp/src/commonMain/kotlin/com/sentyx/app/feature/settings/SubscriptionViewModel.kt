package com.sentyx.app.feature.settings

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.core.ui.ToastData
import com.sentyx.app.core.ui.ToastTone
import com.sentyx.app.domain.model.Plan
import com.sentyx.app.domain.model.PlanTier
import com.sentyx.app.domain.model.SubscriptionUsage
import com.sentyx.app.domain.model.UpgradeResult
import com.sentyx.app.domain.repository.SubscriptionRepository
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch

/** The upgrade bottom-sheet state machine. Null when the sheet is closed. */
sealed interface UpgradeStep {
    /** Review/confirm the chosen [tier] before starting the trial. */
    data class Review(val tier: PlanTier) : UpgradeStep

    /** Upgrade succeeded; celebrate the new [tier]. */
    data class Success(val tier: PlanTier) : UpgradeStep

    /** Simulated payment decline. */
    data object Declined : UpgradeStep
}

/** UI state for [SubscriptionScreen]'s plan card and tier list. */
data class SubscriptionUiState(
    val usage: SubscriptionUsage,
    val tiers: List<PlanTier>,
    val currentPlan: Plan,
)

/**
 * Backs [SubscriptionScreen]. Exposes usage + tiers and owns the upgrade sheet
 * state machine (Review → Success | Declined). Confirming calls
 * [SubscriptionRepository.upgrade]; "Simulate declined payment" upgrades with
 * `simulateDecline = true`.
 */
class SubscriptionViewModel(
    private val subscription: SubscriptionRepository,
    private val toasts: ToastController,
) : ViewModel() {

    val state: StateFlow<SubscriptionUiState> =
        subscription.usage
            .map { usage ->
                SubscriptionUiState(
                    usage = usage,
                    tiers = subscription.tiers,
                    currentPlan = usage.plan,
                )
            }
            .stateIn(
                viewModelScope,
                SharingStarted.WhileSubscribed(5_000),
                SubscriptionUiState(
                    usage = subscription.usage.value,
                    tiers = subscription.tiers,
                    currentPlan = subscription.usage.value.plan,
                ),
            )

    private val _upgrade = MutableStateFlow<UpgradeStep?>(null)
    val upgrade: StateFlow<UpgradeStep?> = _upgrade.asStateFlow()

    private var busy = false

    /** Opens the review sheet for [tier] ("Choose <name>"). */
    fun openUpgrade(tier: PlanTier) {
        _upgrade.value = UpgradeStep.Review(tier)
    }

    /** "Start free trial": upgrades to the reviewed tier, then shows Success. */
    fun confirm() {
        val step = _upgrade.value as? UpgradeStep.Review ?: return
        if (busy) return
        busy = true
        viewModelScope.launch {
            try {
                when (subscription.upgrade(step.tier.plan)) {
                    is UpgradeResult.Success -> _upgrade.value = UpgradeStep.Success(step.tier)
                    UpgradeResult.PaymentDeclined -> _upgrade.value = UpgradeStep.Declined
                }
            } finally {
                busy = false
            }
        }
    }

    /** "Simulate declined payment": forces the declined outcome. */
    fun simulateDeclined() {
        val step = _upgrade.value as? UpgradeStep.Review ?: return
        if (busy) return
        busy = true
        viewModelScope.launch {
            try {
                subscription.upgrade(step.tier.plan, simulateDecline = true)
                _upgrade.value = UpgradeStep.Declined
            } finally {
                busy = false
            }
        }
    }

    /** Closes the sheet (Not now / Done / Close). */
    fun close() {
        _upgrade.value = null
    }

    /** "Manage plan" on the current tier: mock action toast. */
    fun managePlan() {
        toasts.show(
            ToastData(
                title = "Mock action",
                subtitle = "Not wired up in this prototype",
                tone = ToastTone.Info,
            ),
        )
    }

    fun restorePurchases() {
        viewModelScope.launch {
            subscription.restorePurchases()
            toasts.show(
                ToastData(
                    title = "Purchases restored",
                    subtitle = "Nothing to restore",
                    tone = ToastTone.Info,
                ),
            )
        }
    }
}

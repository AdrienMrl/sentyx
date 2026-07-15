package com.sentyx.app.domain.repository

import com.sentyx.app.domain.model.AppPrefs
import com.sentyx.app.domain.model.NotificationEntry
import com.sentyx.app.domain.model.NotificationPrefs
import com.sentyx.app.domain.model.PermissionStatus
import com.sentyx.app.domain.model.Plan
import com.sentyx.app.domain.model.PlanTier
import com.sentyx.app.domain.model.QuietHours
import com.sentyx.app.domain.model.Severity
import com.sentyx.app.domain.model.SubscriptionUsage
import com.sentyx.app.domain.model.UpgradeResult
import kotlinx.coroutines.flow.StateFlow

/** Notification rules, quiet hours, and history. */
interface NotificationSettingsRepository {
    val prefs: StateFlow<NotificationPrefs>
    val history: StateFlow<List<NotificationEntry>>

    suspend fun setRuleEnabled(key: String, enabled: Boolean)
    suspend fun setMinSeverity(severity: Severity)
    suspend fun setQuietHours(quietHours: QuietHours)
    suspend fun setHidePreviewContent(hide: Boolean)
}

/** Subscription state and purchase flow. */
interface SubscriptionRepository {
    val usage: StateFlow<SubscriptionUsage>
    val tiers: List<PlanTier>

    /** Attempts an upgrade; demo impl can simulate declines. */
    suspend fun upgrade(plan: Plan, simulateDecline: Boolean = false): UpgradeResult
    suspend fun restorePurchases()
}

/** App-level preferences + phone permission status. */
interface AppSettingsRepository {
    val prefs: StateFlow<AppPrefs>
    val permissions: StateFlow<List<PermissionStatus>>
}

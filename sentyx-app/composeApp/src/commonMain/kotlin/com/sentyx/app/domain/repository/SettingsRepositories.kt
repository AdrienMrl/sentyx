package com.sentyx.app.domain.repository

import com.sentyx.app.domain.model.AppPrefs
import com.sentyx.app.domain.model.MinThreatLevel
import com.sentyx.app.domain.model.NotificationEntry
import com.sentyx.app.domain.model.NotificationPrefs
import com.sentyx.app.domain.model.PermissionStatus
import com.sentyx.app.domain.model.Plan
import com.sentyx.app.domain.model.PlanTier
import com.sentyx.app.domain.model.QuietHours
import com.sentyx.app.domain.model.SubscriptionUsage
import com.sentyx.app.domain.model.UpgradeResult
import kotlinx.coroutines.flow.StateFlow

/** Push-notification threshold (server-backed), plus rules, quiet hours, and history. */
interface NotificationSettingsRepository {
    /**
     * The server-backed push threshold. Real impls load this from
     * `/v1/me/notification-settings`; the demo impl keeps it in memory.
     */
    val minThreatLevel: StateFlow<MinThreatLevel>

    val prefs: StateFlow<NotificationPrefs>
    val history: StateFlow<List<NotificationEntry>>

    /**
     * Set the push threshold. Implementations update [minThreatLevel] optimistically
     * and persist to the backend; on failure they roll [minThreatLevel] back and
     * throw so the caller can surface an error. The demo impl never throws.
     */
    suspend fun setMinThreatLevel(level: MinThreatLevel)

    suspend fun setRuleEnabled(key: String, enabled: Boolean)
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

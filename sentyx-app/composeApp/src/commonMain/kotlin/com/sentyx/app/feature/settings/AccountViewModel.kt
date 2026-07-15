package com.sentyx.app.feature.settings

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.core.ui.ToastData
import com.sentyx.app.core.ui.ToastTone
import com.sentyx.app.domain.model.Plan
import com.sentyx.app.domain.model.SessionInfo
import com.sentyx.app.domain.model.UserProfile
import com.sentyx.app.domain.repository.AuthRepository
import com.sentyx.app.domain.repository.SubscriptionRepository
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/** UI state shared by the Account, Security, and Sessions screens. */
data class AccountUiState(
    /** Null while signed out; screens fall back to design defaults. */
    val profile: UserProfile?,
    val sessions: List<SessionInfo>,
    /** Current plan name for the billing row, e.g. "Free". */
    val planName: String,
    /** Two-factor toggle state (Security screen). */
    val twoFactorEnabled: Boolean,
)

/**
 * Backs [AccountScreen], [SecurityScreen], and [SessionsScreen]. Exposes the
 * profile, signed-in sessions, current plan name, and the two-factor toggle;
 * runs restore-purchases and session revocation.
 */
class AccountViewModel(
    private val auth: AuthRepository,
    private val subscription: SubscriptionRepository,
    private val toasts: ToastController,
) : ViewModel() {

    // Local (demo) two-factor state — no repository method persists it.
    private val twoFactor = MutableStateFlow(auth.profile.value?.twoFactorEnabled ?: true)

    val state: StateFlow<AccountUiState> =
        combine(
            auth.profile,
            auth.sessions,
            subscription.usage,
            twoFactor,
        ) { profile, sessions, usage, twoFA ->
            AccountUiState(
                profile = profile,
                sessions = sessions,
                planName = planName(usage.plan),
                twoFactorEnabled = twoFA,
            )
        }.stateIn(
            viewModelScope,
            SharingStarted.WhileSubscribed(5_000),
            AccountUiState(
                profile = auth.profile.value,
                sessions = auth.sessions.value,
                planName = planName(subscription.usage.value.plan),
                twoFactorEnabled = twoFactor.value,
            ),
        )

    /** "Name & email" row: mock action toast. */
    fun editNameEmail() = stub()

    /** "Change password" row (Security screen): mock action toast. */
    fun changePassword() = stub()

    fun setTwoFactorEnabled(enabled: Boolean) = twoFactor.update { enabled }

    /** Restore purchases: runs the repo call, then the design's result toast. */
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

    fun revokeSession(id: String) {
        viewModelScope.launch { auth.revokeSession(id) }
    }

    private fun stub() {
        toasts.show(
            ToastData(
                title = "Mock action",
                subtitle = "Not wired up in this prototype",
                tone = ToastTone.Info,
            ),
        )
    }

    private fun planName(plan: Plan): String =
        subscription.tiers.first { it.plan == plan }.name
}

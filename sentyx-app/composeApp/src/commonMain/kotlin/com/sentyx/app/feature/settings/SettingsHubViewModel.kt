package com.sentyx.app.feature.settings

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.core.ui.ToastData
import com.sentyx.app.core.ui.ToastTone
import com.sentyx.app.domain.model.Plan
import com.sentyx.app.domain.repository.AuthRepository
import com.sentyx.app.domain.repository.SubscriptionRepository
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch

/** UI state for the settings hub root. */
data class SettingsHubUiState(
    /** Signed-in account email (Account row subtitle); empty when signed out. */
    val email: String,
    /** Current plan name (Subscription row subtitle), e.g. "Free". */
    val planName: String,
    /**
     * Count for the "N active" transfers subtitle. Static 0: the mandated
     * constructor does not inject the transfer repository, so a live count is
     * not observable here. Widen the constructor later to make it live.
     */
    val activeTransfers: Int,
)

/**
 * Drives [SettingsHubScreen]. Surfaces the account email and current plan name
 * for the list subtitles, the "Help & support" toast, and sign-out. Navigation
 * is delegated to the screen's callbacks.
 */
class SettingsHubViewModel(
    private val auth: AuthRepository,
    private val subscription: SubscriptionRepository,
    private val toasts: ToastController,
) : ViewModel() {

    val state: StateFlow<SettingsHubUiState> =
        combine(auth.profile, subscription.usage) { profile, usage ->
            SettingsHubUiState(
                email = profile?.email ?: "",
                planName = planName(usage.plan),
                activeTransfers = 0,
            )
        }.stateIn(
            viewModelScope,
            SharingStarted.WhileSubscribed(5_000),
            SettingsHubUiState(
                email = auth.profile.value?.email ?: "",
                planName = planName(subscription.usage.value.plan),
                activeTransfers = 0,
            ),
        )

    /** Help & support row: surfaces the design's mock-link toast. */
    fun helpAndSupport() {
        toasts.show(
            ToastData(
                title = "Help center",
                subtitle = "Mock link",
                tone = ToastTone.Info,
            ),
        )
    }

    /** Signs out, then invokes [onSignedOut] (the screen's navigation lambda). */
    fun signOut(onSignedOut: () -> Unit) {
        viewModelScope.launch {
            auth.signOut()
            onSignedOut()
        }
    }

    private fun planName(plan: Plan): String =
        subscription.tiers.first { it.plan == plan }.name
}

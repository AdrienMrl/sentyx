package com.sentyx.app.feature.onboarding

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.sentyx.app.core.ui.ToastController
import com.sentyx.app.core.ui.ToastData
import com.sentyx.app.core.ui.ToastTone
import com.sentyx.app.domain.repository.AuthRepository
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/**
 * Immutable UI state shared by the onboarding screens. Fields are prefilled with
 * the design's demo values so the flow completes with a single tap, but every
 * input is genuinely editable.
 */
data class AuthUiState(
    val name: String = "Alex Rivera",
    val email: String = "alex@example.com",
    val password: String = "password",
    /** Six-digit verification code; prefilled with the design's demo code. */
    val code: String = "482913",
    /** True while a suspend auth call is in flight; screens disable CTAs. */
    val loading: Boolean = false,
)

/**
 * Drives Welcome / Sign in / Create account / Verify email / Reset password.
 * Holds the editable field values and runs the [AuthRepository] calls in
 * [viewModelScope]. It never navigates itself — each action takes an
 * `onSuccess` callback the screen wires to its navigation lambda.
 */
class AuthViewModel(
    private val auth: AuthRepository,
    private val toasts: ToastController,
) : ViewModel() {

    private val _state = MutableStateFlow(AuthUiState())
    val state: StateFlow<AuthUiState> = _state.asStateFlow()

    fun updateName(value: String) = _state.update { it.copy(name = value) }
    fun updateEmail(value: String) = _state.update { it.copy(email = value) }
    fun updatePassword(value: String) = _state.update { it.copy(password = value) }

    /** Keeps only digits, capped at six — for the OTP cells. */
    fun updateCode(value: String) =
        _state.update { it.copy(code = value.filter(Char::isDigit).take(6)) }

    fun signIn(onSuccess: () -> Unit) = runAuth(onSuccess) {
        val s = _state.value
        auth.signIn(s.email, s.password)
    }

    fun createAccount(onSuccess: () -> Unit) = runAuth(onSuccess) {
        val s = _state.value
        auth.createAccount(s.name, s.email, s.password)
    }

    fun verifyEmail(onSuccess: () -> Unit) = runAuth(onSuccess) {
        auth.verifyEmail(_state.value.code)
    }

    /** Resends the verification code; surfaces an info toast (no repo method). */
    fun resendCode() {
        toasts.show(
            ToastData(
                title = "Code resent",
                subtitle = "Check ${_state.value.email}",
                tone = ToastTone.Info,
            ),
        )
    }

    /**
     * Sends the reset link, shows the success toast the design specifies, then
     * invokes [onSent] (the screen's onBack).
     */
    fun sendPasswordReset(onSent: () -> Unit) {
        if (_state.value.loading) return
        val email = _state.value.email
        _state.update { it.copy(loading = true) }
        viewModelScope.launch {
            try {
                auth.sendPasswordReset(email)
                toasts.show(
                    ToastData(
                        title = "Reset link sent",
                        subtitle = "Check $email",
                        tone = ToastTone.Success,
                    ),
                )
                onSent()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                showError(e)
            } finally {
                _state.update { it.copy(loading = false) }
            }
        }
    }

    private fun runAuth(onSuccess: () -> Unit, block: suspend () -> Unit) {
        if (_state.value.loading) return
        _state.update { it.copy(loading = true) }
        viewModelScope.launch {
            try {
                block()
                onSuccess()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                showError(e)
            } finally {
                _state.update { it.copy(loading = false) }
            }
        }
    }

    private fun showError(e: Throwable) {
        toasts.show(
            ToastData(
                title = "Something went wrong",
                subtitle = e.message ?: "Please try again",
                tone = ToastTone.Urgent,
            ),
        )
    }
}

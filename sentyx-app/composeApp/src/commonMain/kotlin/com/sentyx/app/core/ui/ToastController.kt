package com.sentyx.app.core.ui

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

/** In-app toast/banner shown at the top of the screen. */
data class ToastData(
    val title: String,
    val subtitle: String,
    /** Hex or token color for the pulsing dot. */
    val tone: ToastTone,
    /** Trailing call-to-action label, empty for none. */
    val cta: String = "",
    /** Invoked when the toast is tapped. */
    val onTap: (() -> Unit)? = null,
)

enum class ToastTone { Info, Success, Urgent }

/** Single source of truth for the app-wide toast. Auto-dismisses. */
class ToastController(private val scope: CoroutineScope) {
    private val _toast = MutableStateFlow<ToastData?>(null)
    val toast: StateFlow<ToastData?> = _toast

    private var dismissJob: Job? = null

    fun show(toast: ToastData, durationMs: Long = 4200) {
        dismissJob?.cancel()
        _toast.value = toast
        dismissJob = scope.launch {
            delay(durationMs)
            _toast.value = null
        }
    }

    fun dismiss() {
        dismissJob?.cancel()
        _toast.value = null
    }
}

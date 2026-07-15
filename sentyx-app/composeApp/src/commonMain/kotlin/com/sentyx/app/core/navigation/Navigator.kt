package com.sentyx.app.core.navigation

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue

/**
 * Minimal stack navigator matching the design's semantics: push within a tab,
 * switching tabs resets the stack, back pops (or falls back to Events).
 *
 * Deliberately hand-rolled: the app's navigation is a plain stack, and this
 * keeps the surface small and testable. Swap for a library navigator behind
 * the same call sites if deep-linking/state restoration is needed later.
 */
class Navigator(initial: Route = Route.Welcome) {
    var current: Route by mutableStateOf(initial)
        private set

    private val stack = ArrayDeque<Route>()

    val showTabBar: Boolean get() = current.isTabRoot

    fun go(route: Route) {
        stack.addLast(current)
        current = route
    }

    fun back() {
        current = stack.removeLastOrNull() ?: Route.Events
    }

    /** Switches to a tab root, clearing the back stack. */
    fun switchTab(root: Route) {
        require(root.isTabRoot) { "switchTab requires a tab root, got $root" }
        stack.clear()
        current = root
    }

    /** Resets to a flow root (e.g. after sign-out or finishing onboarding). */
    fun reset(route: Route) {
        stack.clear()
        current = route
    }
}

package com.sentyx.app.core.platform

/**
 * Wall-clock time in epoch milliseconds. expect/actual so the shared code can
 * humanize "last seen" without pulling in a datetime dependency.
 */
expect fun currentEpochMillis(): Long

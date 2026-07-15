package com.sentyx.app.domain.model

data class UserProfile(
    val name: String,
    val email: String,
    /** Avatar initials, e.g. "AR". */
    val initials: String,
    val twoFactorEnabled: Boolean,
)

/** A signed-in session on some device (for the sessions screen). */
data class SessionInfo(
    val id: String,
    val deviceName: String,
    val locationLine: String,
    val isCurrent: Boolean,
    /** Emoji glyph for the device kind. */
    val icon: String,
)

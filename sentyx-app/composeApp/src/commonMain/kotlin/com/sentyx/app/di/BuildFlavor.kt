package com.sentyx.app.di

/**
 * Compile-time switch between the demo wiring (default) and the real
 * BLE + backend wiring.
 *
 * To run against a real Sentyx Pi and backend:
 *  1. set [useRealPairing] = true, and
 *  2. set [appConfig] to a non-null [AppConfig] with your server URL and
 *     operator token.
 *
 * Selecting the real flavor with a null/blank [appConfig] fails fast (see
 * [requireRealConfig]) rather than silently falling back to a default.
 */
object BuildFlavor {
    /** Default false so the app runs on demo data out of the box. */
    val useRealPairing: Boolean = true

    /**
     * Required when [useRealPairing] is true; must stay non-null with both
     * fields filled. Example:
     * `AppConfig("https://teslcam.161-35-232-246.sslip.io", "<operator-token>")`.
     */
    val appConfig: AppConfig? = AppConfig(
        serverBaseUrl = "https://teslcam.161-35-232-246.sslip.io",
        operatorToken = "b0ef519dc682144c4b2e7a4258f905b21a13182fffc12f4fd5cf8e950e4a0607",
    )

    /**
     * The [AppConfig] to use for the real flavor, or throw with a clear message
     * if it was left unset. Never returns a default.
     */
    fun requireRealConfig(): AppConfig = appConfig
        ?: error(
            "BuildFlavor.useRealPairing is true but BuildFlavor.appConfig is null. " +
                "Set appConfig to an AppConfig(serverBaseUrl, operatorToken) to use the real wiring.",
        )
}

package com.sentyx.app.di

/**
 * Runtime configuration required by the real (non-demo) app wiring. Both values
 * are mandatory — there are no implicit defaults; construction fails fast if
 * either is blank so a misconfigured build never silently talks to the wrong
 * backend or with no credentials.
 *
 * @param serverBaseUrl base URL of the Sentyx backend (e.g.
 *   `https://teslcam.161-35-232-246.sslip.io`); written into each Pi's config
 *   as its ingest server.
 * @param operatorToken operator bearer token used to register newly paired Pis.
 */
data class AppConfig(
    val serverBaseUrl: String,
    val operatorToken: String,
) {
    init {
        require(serverBaseUrl.isNotBlank()) { "AppConfig.serverBaseUrl must not be blank" }
        require(operatorToken.isNotBlank()) { "AppConfig.operatorToken must not be blank" }
    }
}

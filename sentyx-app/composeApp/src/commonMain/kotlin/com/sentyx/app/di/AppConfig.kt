package com.sentyx.app.di

/**
 * Runtime configuration required by the real (non-demo) app wiring. Every value
 * is mandatory — there are no implicit defaults; construction fails fast if any
 * is blank so a misconfigured build never silently talks to the wrong backend or
 * with no credentials.
 *
 * @param serverBaseUrl base URL of the Sentyx backend (e.g.
 *   `https://teslcam.161-35-232-246.sslip.io`); written into each Pi's config
 *   as its ingest server, and the target of the authenticated app→server calls.
 * @param supabaseUrl base URL of the Supabase project used for user
 *   authentication (GoTrue), e.g. `https://xxxx.supabase.co`.
 * @param supabaseAnonKey the Supabase publishable/anon key (`sb_publishable_…`
 *   or the legacy anon JWT) sent as the `apikey` header on every GoTrue call.
 */
data class AppConfig(
    val serverBaseUrl: String,
    val supabaseUrl: String,
    val supabaseAnonKey: String,
) {
    init {
        require(serverBaseUrl.isNotBlank()) { "AppConfig.serverBaseUrl must not be blank" }
        require(supabaseUrl.isNotBlank()) { "AppConfig.supabaseUrl must not be blank" }
        require(supabaseAnonKey.isNotBlank()) { "AppConfig.supabaseAnonKey must not be blank" }
    }
}

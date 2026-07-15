package com.sentyx.app.core.storage

import android.content.Context

/**
 * Process-wide Application context holder for commonMain-constructed platform
 * code that needs a Context (here, [KeyValueStore]). `MainActivity.onCreate`
 * calls [init] before the app container is built, mirroring how
 * AndroidPermissionsController is created from the Activity. [require] throws if
 * init was skipped — there is no implicit default Context.
 */
object SentyxAppContext {
    private var appContext: Context? = null

    fun init(context: Context) {
        appContext = context.applicationContext
    }

    fun require(): Context = appContext
        ?: error(
            "SentyxAppContext.init(context) was not called. Call it in MainActivity.onCreate " +
                "before the app container is created.",
        )
}

actual class KeyValueStore actual constructor() {
    private val prefs = SentyxAppContext.require()
        .getSharedPreferences("sentyx.kv", Context.MODE_PRIVATE)

    actual fun getString(key: String): String? = prefs.getString(key, null)

    actual fun putString(key: String, value: String) {
        prefs.edit().putString(key, value).apply()
    }

    actual fun remove(key: String) {
        prefs.edit().remove(key).apply()
    }
}

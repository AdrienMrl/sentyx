package com.sentyx.app.core.storage

/**
 * Minimal cross-platform key/value string store. Hand-rolled expect/actual (no
 * third-party settings library): SharedPreferences on Android, NSUserDefaults on
 * iOS. Used to remember the paired device across launches.
 *
 * On Android the actual reads the process Context from [SentyxAppContext], which
 * `MainActivity` populates in `onCreate` (mirroring how AndroidPermissionsController
 * is built there). Construction fails fast if that init was skipped — no implicit
 * default Context.
 */
expect class KeyValueStore() {
    fun getString(key: String): String?
    fun putString(key: String, value: String)
    fun remove(key: String)
}

/** Keys persisted in [KeyValueStore]; shared by the pairing writer and the device reader. */
object StorageKeys {
    const val DEVICE_ID: String = "deviceId"
    const val DEVICE_NAME: String = "deviceName"

    /** JSON blob of per-event user annotations (favorite/reviewed/override/feedback). */
    const val EVENT_META: String = "eventMeta"
}

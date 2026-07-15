package com.sentyx.app.data.ble

import com.sentyx.app.domain.model.CurrentWifi
import com.sentyx.app.domain.model.SavedWifiNetwork
import com.sentyx.app.domain.model.WifiNetwork
import com.sentyx.app.domain.model.WifiStatus

/** Coarse strength word for a 0-100 signal, matching the onboarding copy style. */
internal fun signalWord(signal: Int): String = when {
    signal >= 67 -> "strong"
    signal >= 34 -> "good"
    else -> "weak"
}

/** Map a scan entry to the shared [WifiNetwork] UI model (secured ⇒ needs a password). */
internal fun WifiScanDto.toWifiNetwork(): WifiNetwork {
    val secured = security != "open"
    val securityLabel = if (secured) "Secured" else "Open"
    val savedLabel = if (saved) "Saved · " else ""
    return WifiNetwork(
        ssid = ssid,
        subtitle = "$savedLabel$securityLabel · ${signalWord(signal)}",
        requiresPassword = secured,
    )
}

/** Map a `status` response to the domain [WifiStatus]. */
internal fun WifiResultDto.toWifiStatus(): WifiStatus = WifiStatus(
    current = current?.let { CurrentWifi(ssid = it.ssid, signal = it.signal) },
    saved = saved.map { SavedWifiNetwork(ssid = it.ssid, active = it.active, autoconnect = it.autoconnect) },
)

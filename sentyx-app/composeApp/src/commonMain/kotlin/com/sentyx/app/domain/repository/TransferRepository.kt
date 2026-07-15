package com.sentyx.app.domain.repository

import com.sentyx.app.domain.model.Transfer
import com.sentyx.app.domain.model.TransferRoute
import com.sentyx.app.domain.model.TransferSettings
import kotlinx.coroutines.flow.StateFlow

/** Clip downloads to this phone plus transfer preferences. */
interface TransferRepository {
    /** All transfers, newest first. Completed ones form the local library. */
    val transfers: StateFlow<List<Transfer>>

    val settings: StateFlow<TransferSettings>

    /** Which routes are currently usable (e.g. BT off ⇒ Bluetooth unavailable). */
    fun availableRoutes(): List<Pair<TransferRoute, Boolean>>

    /** Starts a download of an event's original clip; returns the transfer id. */
    suspend fun start(eventId: String, title: String, sizeLabel: String, route: TransferRoute): String

    suspend fun pause(id: String)
    suspend fun resume(id: String)
    suspend fun retry(id: String)
    suspend fun cancel(id: String)
    suspend fun remove(id: String)

    suspend fun updateSettings(settings: TransferSettings)
}

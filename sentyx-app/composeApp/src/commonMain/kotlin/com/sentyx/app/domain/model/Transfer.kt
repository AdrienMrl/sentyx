package com.sentyx.app.domain.model

/** How a clip travels from the car/backend to the phone. */
enum class TransferRoute(val label: String) {
    WifiDirect("Direct Wi-Fi"),
    WifiLocal("Home Wi-Fi"),
    Bluetooth("Bluetooth"),
}

enum class TransferStatus { Queued, Connecting, Transferring, Paused, Complete, Failed, Canceled }

/** A clip download from car/backend to this phone. */
data class Transfer(
    val id: String,
    val eventId: String?,
    val title: String,
    val sizeLabel: String,
    val route: TransferRoute,
    val status: TransferStatus,
    /** 0..100. */
    val progressPct: Float,
    /** e.g. "12.4 MB/s", null when not transferring. */
    val speedLabel: String? = null,
)

/** User preferences controlling automatic transfer behavior. */
data class TransferSettings(
    val preferredRoute: TransferRoute,
    val autoUploadNewClips: Boolean,
    val onlyMostRelevantCamera: Boolean,
    val allowCellularDownloads: Boolean,
)

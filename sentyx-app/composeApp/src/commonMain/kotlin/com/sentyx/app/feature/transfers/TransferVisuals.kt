package com.sentyx.app.feature.transfers

import androidx.compose.ui.graphics.Color
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.domain.model.Transfer
import com.sentyx.app.domain.model.TransferStatus
import kotlin.math.roundToInt

/** Rendering-ready view of a [Transfer]: label, status colors, progress bar. */
internal data class TransferVisual(
    val statusLabel: String,
    /** Color for the status line and the right-aligned percent label. */
    val statusColor: Color,
    val barColor: Color,
    val showBar: Boolean,
    val pctLabel: String,
    /** Progress fraction 0f..1f for [com.sentyx.app.core.designsystem.SxProgressBar]. */
    val pctFraction: Float,
)

/** Maps a [Transfer]'s status to its label and colors, per the design queue spec. */
internal fun Transfer.visual(): TransferVisual {
    val (label, color) = when (status) {
        TransferStatus.Transferring ->
            (speedLabel?.let { "Transferring · $it" } ?: "Transferring") to SxColors.AmberDeep
        TransferStatus.Connecting -> "Connecting…" to SxColors.AmberDeep
        TransferStatus.Paused -> "Paused" to SxColors.Muted
        TransferStatus.Complete -> "Complete" to SxColors.Green
        TransferStatus.Failed -> "Failed · connection lost" to SxColors.RedDeep
        TransferStatus.Canceled -> "Canceled" to SxColors.Muted
        TransferStatus.Queued -> "Queued" to SxColors.Muted
    }
    val barColor = when (status) {
        TransferStatus.Failed -> SxColors.Red
        TransferStatus.Complete -> SxColors.Green
        else -> SxColors.Bronze
    }
    return TransferVisual(
        statusLabel = label,
        statusColor = color,
        barColor = barColor,
        showBar = status != TransferStatus.Complete && status != TransferStatus.Canceled,
        pctLabel = "${progressPct.roundToInt()}%",
        pctFraction = progressPct / 100f,
    )
}

/** Per-status text actions for a queue card, in display order (label → onClick). */
internal fun queueActions(
    transfer: Transfer,
    vm: TransfersViewModel,
): List<Pair<String, () -> Unit>> = when (transfer.status) {
    TransferStatus.Transferring -> listOf(
        "Pause" to { vm.pause(transfer.id) },
        "Cancel" to { vm.cancel(transfer.id) },
    )
    TransferStatus.Paused -> listOf(
        "Resume" to { vm.resume(transfer.id) },
        "Cancel" to { vm.cancel(transfer.id) },
    )
    TransferStatus.Failed -> listOf(
        "Retry" to { vm.retry(transfer.id) },
        "Remove" to { vm.remove(transfer.id) },
    )
    TransferStatus.Complete -> listOf(
        "Play" to { vm.play() },
        "Remove" to { vm.remove(transfer.id) },
    )
    else -> listOf(
        "Remove" to { vm.remove(transfer.id) },
    )
}

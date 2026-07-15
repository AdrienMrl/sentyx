package com.sentyx.app.data.demo

import com.sentyx.app.domain.model.Transfer
import com.sentyx.app.domain.model.TransferRoute
import com.sentyx.app.domain.model.TransferSettings
import com.sentyx.app.domain.model.TransferStatus
import com.sentyx.app.domain.repository.DeviceRepository
import com.sentyx.app.domain.repository.TransferRepository
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlin.random.Random

/**
 * Demo [TransferRepository]. Ports the prototype's download-queue simulation: a
 * started transfer ticks up ~3–7% every 220 ms until complete. The ticker only
 * runs while at least one transfer is in progress and stops itself otherwise.
 */
class DemoTransferRepository(
    private val scope: CoroutineScope,
    private val deviceRepository: DeviceRepository,
) : TransferRepository {

    private val _transfers = MutableStateFlow<List<Transfer>>(emptyList())
    override val transfers: StateFlow<List<Transfer>> = _transfers

    private val _settings = MutableStateFlow(
        TransferSettings(
            preferredRoute = TransferRoute.WifiDirect,
            autoUploadNewClips = true,
            onlyMostRelevantCamera = true,
            allowCellularDownloads = false,
        ),
    )
    override val settings: StateFlow<TransferSettings> = _settings

    private var idCounter = 0
    private var tickJob: Job? = null

    override fun availableRoutes(): List<Pair<TransferRoute, Boolean>> {
        val bleOn = deviceRepository.device.value?.bleConnected == true
        return listOf(
            TransferRoute.WifiDirect to true,
            TransferRoute.WifiLocal to true,
            TransferRoute.Bluetooth to bleOn,
        )
    }

    override suspend fun start(
        eventId: String,
        title: String,
        sizeLabel: String,
        route: TransferRoute,
    ): String {
        val id = "q${++idCounter}"
        val transfer = Transfer(
            id = id,
            eventId = eventId,
            title = title,
            sizeLabel = sizeLabel,
            route = route,
            status = TransferStatus.Transferring,
            progressPct = 0f,
            speedLabel = SPEED,
        )
        _transfers.update { listOf(transfer) + it }
        ensureTicker()
        return id
    }

    override suspend fun pause(id: String) = setStatus(id, TransferStatus.Paused)

    override suspend fun resume(id: String) {
        setStatus(id, TransferStatus.Transferring)
        ensureTicker()
    }

    override suspend fun retry(id: String) {
        setStatus(id, TransferStatus.Transferring)
        ensureTicker()
    }

    override suspend fun cancel(id: String) = setStatus(id, TransferStatus.Canceled)

    override suspend fun remove(id: String) {
        _transfers.update { list -> list.filterNot { it.id == id } }
    }

    override suspend fun updateSettings(settings: TransferSettings) {
        _settings.value = settings
    }

    private fun setStatus(id: String, status: TransferStatus) {
        _transfers.update { list ->
            list.map {
                if (it.id != id) it
                else it.copy(
                    status = status,
                    speedLabel = if (status == TransferStatus.Transferring) SPEED else null,
                )
            }
        }
    }

    private fun ensureTicker() {
        if (tickJob?.isActive == true) return
        tickJob = scope.launch {
            while (isActive) {
                delay(220)
                var anyActive = false
                _transfers.update { list ->
                    list.map { t ->
                        if (t.status == TransferStatus.Transferring && t.progressPct < 100f) {
                            val next = minOf(100f, t.progressPct + 3f + Random.nextInt(0, 5))
                            val done = next >= 100f
                            if (!done) anyActive = true
                            t.copy(
                                progressPct = next,
                                status = if (done) TransferStatus.Complete else TransferStatus.Transferring,
                                speedLabel = if (done) null else SPEED,
                            )
                        } else {
                            t
                        }
                    }
                }
                if (!anyActive) break
            }
            tickJob = null
        }
    }

    private companion object {
        const val SPEED = "12.4 MB/s"
    }
}

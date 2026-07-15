package com.sentyx.app.feature.events

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.foundation.Image
import androidx.compose.runtime.getValue
import androidx.compose.runtime.produceState
import androidx.compose.ui.graphics.ImageBitmap
import com.sentyx.app.data.thumbnail.EventThumbnailLoader
import org.jetbrains.compose.resources.painterResource
import sentyxapp.composeapp.generated.resources.Res
import sentyxapp.composeapp.generated.resources.device_car
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.sentyx.app.core.designsystem.SxCard
import com.sentyx.app.core.designsystem.SxChip
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxEmptyState
import com.sentyx.app.core.designsystem.SxSpinner
import com.sentyx.app.core.designsystem.StatusPill
import com.sentyx.app.core.designsystem.monoFamily
import com.sentyx.app.domain.model.DeviceSnapshot
import com.sentyx.app.domain.model.EventWithMeta
import com.sentyx.app.domain.model.FeedFilter
import com.sentyx.app.domain.model.FeedGroup
import com.sentyx.app.domain.model.FeedState
import com.sentyx.app.domain.model.Severity

/**
 * Events feed (tab root). Header + car status tile + filter chips over a
 * timeline-grouped feed, with select-mode multi-select and content states
 * (loading / empty / offline / error). Navigation is exclusively via callbacks.
 */
@Composable
fun EventsFeedScreen(
    vm: EventsFeedViewModel,
    onOpenEvent: (String) -> Unit,
    onOpenDevice: () -> Unit,
    onGoToTransfers: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()
    val scroll = rememberScrollState()

    Box(Modifier.fillMaxSize().background(SxColors.Bg)) {
        Column(
            Modifier
                .fillMaxSize()
                .verticalScroll(scroll)
                .padding(top = 56.dp, bottom = 96.dp),
        ) {
            // Header + car tile + search + chips share the 22dp horizontal inset.
            Column(
                Modifier.padding(horizontal = 22.dp, vertical = 0.dp),
                verticalArrangement = Arrangement.spacedBy(15.dp),
            ) {
                Spacer(Modifier.height(8.dp))
                FeedHeader(
                    selectLabel = state.selectLabel,
                    onToggleSelect = vm::toggleSelectMode,
                    onToggleSearch = vm::toggleSearch,
                )
                CarStatusTile(device = state.device, todayCount = state.todayCount, onClick = onOpenDevice)
                if (state.searchOpen) SearchBar()
                FilterChips(active = state.filter, onPick = vm::setFilter)
            }

            when (val feed = state.feed) {
                FeedState.Loading -> LoadingBlock()
                FeedState.Empty -> SxEmptyState(
                    emoji = "🌿",
                    title = "All quiet",
                    body = "No events today. Sentyx is watching and will alert you if anything happens.",
                    modifier = Modifier.fillMaxWidth().padding(top = 30.dp),
                )
                FeedState.Offline -> SxEmptyState(
                    emoji = "📡",
                    title = "You're offline",
                    body = "Showing cached events. New events will sync when the connection returns.",
                    modifier = Modifier.fillMaxWidth().padding(top = 30.dp),
                )
                FeedState.Error -> SxEmptyState(
                    emoji = "⚠",
                    title = "Couldn't reach Sentyx",
                    body = "The backend is temporarily unavailable. Local events from the Pi are still accessible.",
                    modifier = Modifier.fillMaxWidth().padding(top = 30.dp),
                )
                is FeedState.Loaded -> LoadedFeed(
                    groups = state.displayGroups,
                    selectMode = state.selectMode,
                    selected = state.selected,
                    thumbnails = vm.thumbnails,
                    onCardTap = { id ->
                        if (state.selectMode) vm.toggleSelection(id) else onOpenEvent(id)
                    },
                    onLoadEarlier = vm::loadEarlier,
                )
            }
        }

        if (state.selectBarVisible) {
            SelectActionBar(
                count = state.selectedCount,
                onDownload = { vm.bulkDownload(); onGoToTransfers() },
                onReviewed = vm::bulkReviewed,
                onDelete = vm::bulkDelete,
                modifier = Modifier.align(Alignment.BottomCenter),
            )
        }
    }
}

@Composable
private fun FeedHeader(
    selectLabel: String,
    onToggleSelect: () -> Unit,
    onToggleSearch: () -> Unit,
) {
    Row(
        Modifier.fillMaxWidth(),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.SpaceBetween,
    ) {
        Text(
            "SENTYX",
            color = SxColors.Muted,
            fontSize = 13.sp,
            fontWeight = FontWeight.Bold,
            style = TextStyle(letterSpacing = 3.sp),
        )
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            Text(
                selectLabel,
                color = SxColors.Bronze,
                fontSize = 12.5.sp,
                fontWeight = FontWeight.Bold,
                modifier = Modifier.clickable(onClick = onToggleSelect),
            )
            Box(
                Modifier
                    .size(32.dp)
                    .clip(CircleShape)
                    .background(SxColors.Card)
                    .border(1.dp, SxColors.Border, CircleShape)
                    .clickable(onClick = onToggleSearch),
                contentAlignment = Alignment.Center,
            ) {
                Text("🔍", fontSize = 14.sp)
            }
        }
    }
}

@Composable
private fun CarStatusTile(device: DeviceSnapshot?, todayCount: Int, onClick: () -> Unit) {
    SxCard(onClick = onClick, modifier = Modifier.fillMaxWidth()) {
        Column(verticalArrangement = Arrangement.spacedBy(15.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(14.dp)) {
                CarRender()
                Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    Text(
                        device?.name ?: "No device",
                        color = SxColors.Ink,
                        fontSize = 16.5.sp,
                        fontWeight = FontWeight.ExtraBold,
                        style = TextStyle(letterSpacing = (-0.3).sp),
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                    Text(
                        "Parked · ${device?.location ?: "—"}",
                        color = SxColors.Muted,
                        fontSize = 12.sp,
                    )
                    if (device != null) {
                        val chip = deviceChipStyle(device.condition)
                        StatusPill(
                            text = device.statusText,
                            color = chip.color,
                            bg = chip.bg,
                            pulsing = true,
                            modifier = Modifier.padding(top = 3.dp),
                        )
                    }
                }
            }
            Row(
                Modifier
                    .fillMaxWidth()
                    .height(1.dp)
                    .background(SxColors.Divider),
            ) {}
            Row(Modifier.fillMaxWidth()) {
                StatCell("Sentry", device?.sentryStatus ?: "—", Modifier.weight(1f), divider = false)
                StatCell("Storage", "${device?.storageFreePct ?: 0}% free", Modifier.weight(1f), divider = true)
                StatCell("Today", "$todayCount events", Modifier.weight(1f), divider = true)
            }
        }
    }
}

@Composable
private fun StatCell(label: String, value: String, modifier: Modifier, divider: Boolean) {
    Row(modifier) {
        if (divider) {
            Box(
                Modifier
                    .width(1.dp)
                    .height(30.dp)
                    .background(SxColors.Divider),
            )
        }
        Column(
            Modifier.padding(start = if (divider) 14.dp else 0.dp),
            verticalArrangement = Arrangement.spacedBy(3.dp),
        ) {
            Text(
                label.uppercase(),
                color = SxColors.Faint,
                fontSize = 10.sp,
                fontWeight = FontWeight.Bold,
                style = TextStyle(letterSpacing = 0.6.sp),
            )
            Text(value, color = SxColors.Ink, fontSize = 13.sp, fontWeight = FontWeight.Bold)
        }
    }
}

/** Product illustration of the paired vehicle, on a rounded tile. */
@Composable
private fun CarRender() {
    Image(
        painter = painterResource(Res.drawable.device_car),
        contentDescription = null,
        contentScale = ContentScale.Crop,
        modifier = Modifier
            .size(width = 110.dp, height = 64.dp)
            .clip(RoundedCornerShape(12.dp)),
    )
}

@Composable
private fun SearchBar() {
    Row(
        Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(12.dp))
            .background(SxColors.Card)
            .border(1.dp, SxColors.Sand, RoundedCornerShape(12.dp))
            .padding(horizontal = 13.dp, vertical = 11.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Text("🔍", fontSize = 14.sp)
        Text("Search descriptions & locations…", color = SxColors.Muted, fontSize = 13.5.sp)
    }
}

@Composable
private fun FilterChips(active: FeedFilter, onPick: (FeedFilter) -> Unit) {
    val chips = listOf(
        FeedFilter.All to "All",
        FeedFilter.Urgent to "Urgent",
        FeedFilter.Attention to "Attention",
        FeedFilter.Routine to "Routine",
        FeedFilter.Favorites to "★ Favorites",
    )
    Row(
        Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        chips.forEach { (filter, label) ->
            SxChip(label = label, selected = active == filter, onClick = { onPick(filter) })
        }
    }
}

@Composable
private fun LoadingBlock() {
    Column(
        Modifier.fillMaxWidth().padding(vertical = 60.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        SxSpinner()
        Text("Loading events…", color = SxColors.Muted, fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
    }
}

@Composable
private fun LoadedFeed(
    groups: List<FeedGroup>,
    selectMode: Boolean,
    selected: Set<String>,
    thumbnails: EventThumbnailLoader,
    onCardTap: (String) -> Unit,
    onLoadEarlier: () -> Unit,
) {
    Column(Modifier.fillMaxWidth().padding(top = 8.dp)) {
        groups.forEach { group ->
            Text(
                group.label.uppercase(),
                color = SxColors.Faint,
                fontSize = 12.sp,
                fontWeight = FontWeight.Bold,
                style = TextStyle(letterSpacing = 0.5.sp),
                modifier = Modifier.padding(start = 22.dp, end = 22.dp, top = 16.dp, bottom = 6.dp),
            )
            Column(Modifier.fillMaxWidth().padding(start = 26.dp, end = 22.dp)) {
                group.events.forEach { item ->
                    TimelineRow(
                        item = item,
                        selectMode = selectMode,
                        selected = item.event.id in selected,
                        thumbnails = thumbnails,
                        onTap = { onCardTap(item.event.id) },
                    )
                }
            }
        }
        Box(
            Modifier
                .fillMaxWidth()
                .padding(start = 22.dp, end = 22.dp, top = 6.dp)
                .clip(RoundedCornerShape(13.dp))
                .border(1.dp, SxColors.Border, RoundedCornerShape(13.dp))
                .clickable(onClick = onLoadEarlier)
                .padding(vertical = 13.dp),
            contentAlignment = Alignment.Center,
        ) {
            Text("Load earlier events", color = SxColors.Muted, fontSize = 13.sp, fontWeight = FontWeight.Bold)
        }
    }
}

@Composable
private fun TimelineRow(
    item: EventWithMeta,
    selectMode: Boolean,
    selected: Boolean,
    thumbnails: EventThumbnailLoader,
    onTap: () -> Unit,
) {
    val severity = item.severity
    val nodeColor = severity?.let { com.sentyx.app.core.designsystem.severityColors(it).node } ?: SxColors.Chevron
    Row(Modifier.fillMaxWidth().height(IntrinsicSize.Min), horizontalArrangement = Arrangement.spacedBy(14.dp)) {
        // Timeline node + connector.
        Column(Modifier.width(14.dp).fillMaxHeight(), horizontalAlignment = Alignment.CenterHorizontally) {
            Box(
                Modifier
                    .padding(top = 20.dp)
                    .size(13.dp)
                    .clip(CircleShape)
                    .background(TimelineConnector)
                    .padding(1.dp)
                    .clip(CircleShape)
                    .background(nodeColor),
            )
            Box(Modifier.width(2.dp).weight(1f).background(TimelineConnector))
        }
        EventCard(item = item, selectMode = selectMode, selected = selected, thumbnails = thumbnails, onTap = onTap, modifier = Modifier.weight(1f))
    }
}

@Composable
private fun EventCard(
    item: EventWithMeta,
    selectMode: Boolean,
    selected: Boolean,
    thumbnails: EventThumbnailLoader,
    onTap: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val event = item.event
    val severity = item.severity
    val reviewed = item.meta.reviewed
    val cardBorder = if (severity == Severity.Urgent) UrgentCardBorder else SxColors.Border
    val done = event.state == com.sentyx.app.domain.model.AnalysisState.Complete
    val shape = RoundedCornerShape(16.dp)
    Row(
        modifier
            .padding(bottom = 14.dp)
            .clip(shape)
            .background(SxColors.Card)
            .border(1.dp, cardBorder, shape)
            .clickable(onClick = onTap)
            .padding(14.dp),
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        if (selectMode) SelectCheck(selected)
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(7.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (severity != null) {
                    val c = com.sentyx.app.core.designsystem.severityColors(severity)
                    Box(
                        Modifier
                            .clip(RoundedCornerShape(999.dp))
                            .background(c.bg)
                            .padding(horizontal = 9.dp, vertical = 3.dp),
                    ) {
                        Text(
                            com.sentyx.app.core.designsystem.severityLabel(severity),
                            color = c.fg,
                            fontSize = 10.5.sp,
                            fontWeight = FontWeight.Bold,
                            style = TextStyle(letterSpacing = 0.4.sp),
                        )
                    }
                }
                Text(
                    "${event.time} · ${event.cameras.firstOrNull() ?: "—"}",
                    color = SxColors.Muted,
                    fontSize = 11.5.sp,
                    fontWeight = FontWeight.SemiBold,
                )
                if (item.meta.favorite) Text("★", color = SxColors.Amber, fontSize = 12.sp)
            }
            Text(
                event.title,
                color = if (reviewed) SxColors.Muted else SxColors.Ink,
                fontSize = 15.sp,
                fontWeight = FontWeight.ExtraBold,
                style = TextStyle(letterSpacing = (-0.2).sp),
            )
            Text(event.location, color = SxColors.InkSecondary, fontSize = 12.sp)
            if (done) {
                event.aiSummaryShort?.let {
                    Text(it, color = AiTextColor, fontSize = 12.5.sp)
                }
            } else {
                analysisPillStyle(event.state)?.let { pill ->
                    StatusPill(text = pill.label, color = pill.color, bg = pill.bg, pulsing = pill.pulsing)
                }
            }
        }
        if (event.state != com.sentyx.app.domain.model.AnalysisState.Writing) {
            FeedThumb(eventId = event.id, duration = event.durationLabel, loader = thumbnails)
        }
    }
}

@Composable
private fun SelectCheck(selected: Boolean) {
    Box(
        Modifier
            .size(22.dp)
            .clip(CircleShape)
            .background(if (selected) SxColors.Ink else Color.Transparent)
            .border(2.dp, if (selected) SxColors.Ink else SxColors.Chevron, CircleShape),
        contentAlignment = Alignment.Center,
    ) {
        if (selected) Text("✓", color = SxColors.White, fontSize = 12.sp, fontWeight = FontWeight.Bold)
    }
}

@Composable
private fun FeedThumb(eventId: String, duration: String, loader: EventThumbnailLoader) {
    // Re-fetches when the card is bound to a different event; the loader decodes
    // off the main thread and caches, so a scroll-back is a cache hit. Null (not
    // yet uploaded/analyzed, or fetch failed) falls back to the striped placeholder.
    val bitmap: ImageBitmap? by produceState<ImageBitmap?>(initialValue = null, eventId) {
        value = loader.load(eventId)
    }
    Box(
        Modifier
            .size(width = 66.dp, height = 88.dp)
            .clip(RoundedCornerShape(12.dp))
            .background(Color(0xFFE7E0D2)),
        contentAlignment = Alignment.BottomCenter,
    ) {
        bitmap.let { bmp ->
            if (bmp != null) {
                Image(
                    bitmap = bmp,
                    contentDescription = null,
                    contentScale = ContentScale.Crop,
                    modifier = Modifier.fillMaxSize(),
                )
            } else {
                com.sentyx.app.core.designsystem.StripedThumb(Modifier.fillMaxSize())
            }
        }
        Text(
            duration,
            color = SxColors.Hint,
            fontSize = 7.sp,
            fontFamily = monoFamily,
            fontWeight = FontWeight.Medium,
            modifier = Modifier.padding(bottom = 6.dp),
        )
    }
}

@Composable
private fun SelectActionBar(
    count: Int,
    onDownload: () -> Unit,
    onReviewed: () -> Unit,
    onDelete: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Row(
        modifier
            .fillMaxWidth()
            .padding(start = 12.dp, end = 12.dp, bottom = 96.dp)
            .clip(RoundedCornerShape(16.dp))
            .background(SxColors.Ink)
            .padding(horizontal = 16.dp, vertical = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        Text("$count selected", color = SxColors.OnInk, fontSize = 13.sp, fontWeight = FontWeight.Bold, modifier = Modifier.weight(1f))
        Text("Download", color = SxColors.Gold, fontSize = 12.5.sp, fontWeight = FontWeight.Bold, modifier = Modifier.clickable(onClick = onDownload))
        Text("Reviewed", color = SxColors.Gold, fontSize = 12.5.sp, fontWeight = FontWeight.Bold, modifier = Modifier.clickable(onClick = onReviewed))
        Text("Delete", color = BulkDeleteColor, fontSize = 12.5.sp, fontWeight = FontWeight.Bold, modifier = Modifier.clickable(onClick = onDelete))
    }
}

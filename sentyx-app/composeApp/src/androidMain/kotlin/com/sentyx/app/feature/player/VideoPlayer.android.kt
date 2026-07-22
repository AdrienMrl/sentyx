package com.sentyx.app.feature.player

import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.viewinterop.AndroidView
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.media3.common.MediaItem
import androidx.media3.common.util.UnstableApi
import androidx.media3.datasource.DefaultHttpDataSource
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.DefaultMediaSourceFactory
import androidx.media3.ui.PlayerView
import com.sentyx.app.data.clip.ClipSource

/**
 * Android actual: Media3 ExoPlayer behind a [PlayerView]. The clip streams over
 * an HTTP data source whose default request properties carry [ClipSource.headers]
 * (the bearer token), so both the initial request and every seek range request
 * are authenticated. The player is rebuilt if [source] changes and released when
 * the composable leaves composition; it pauses when the host lifecycle stops.
 *
 * [PlayerView] is still `@UnstableApi` in Media3; the annotation opts this
 * boundary in explicitly rather than suppressing it project-wide.
 */
@UnstableApi
@Composable
actual fun VideoPlayer(
    source: ClipSource,
    modifier: Modifier,
    autoPlay: Boolean,
) {
    val context = LocalContext.current
    val lifecycleOwner = LocalLifecycleOwner.current

    val player = remember(source) {
        val httpFactory = DefaultHttpDataSource.Factory().apply {
            if (source.headers.isNotEmpty()) setDefaultRequestProperties(source.headers)
        }
        ExoPlayer.Builder(context)
            .setMediaSourceFactory(DefaultMediaSourceFactory(httpFactory))
            .build()
            .apply {
                setMediaItem(MediaItem.fromUri(source.url))
                playWhenReady = autoPlay
                prepare()
            }
    }

    // Pause when the host stops (backgrounded / screen off); the buffered
    // position is retained so resume is instant. Release on dispose.
    DisposableEffect(lifecycleOwner, player) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_STOP) player.pause()
        }
        lifecycleOwner.lifecycle.addObserver(observer)
        onDispose {
            lifecycleOwner.lifecycle.removeObserver(observer)
            player.release()
        }
    }

    AndroidView(
        modifier = modifier,
        factory = { ctx ->
            PlayerView(ctx).apply {
                this.player = player
                useController = true
            }
        },
    )
}

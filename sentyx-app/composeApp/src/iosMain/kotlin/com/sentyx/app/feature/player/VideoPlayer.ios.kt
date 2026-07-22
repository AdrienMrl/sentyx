package com.sentyx.app.feature.player

import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.remember
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.Modifier
import androidx.compose.ui.interop.UIKitViewController
import com.sentyx.app.data.clip.ClipSource
import kotlinx.cinterop.ExperimentalForeignApi
import platform.AVFoundation.AVPlayer
import platform.AVFoundation.AVPlayerItem
import platform.AVFoundation.AVURLAsset
import platform.AVFoundation.pause
import platform.AVFoundation.play
import platform.AVKit.AVPlayerViewController
import platform.Foundation.NSURL

/**
 * iOS actual: an [AVPlayerViewController] (native transport controls) hosted via
 * Compose's [UIKitViewController] interop. The clip's bearer header is passed to
 * [AVURLAsset] through the `AVURLAssetHTTPHeaderFieldsKey` option (a documented
 * key that isn't surfaced in the Kotlin/Native AVFoundation bindings, so it is
 * spelled as its string literal), so AVFoundation attaches it to the initial
 * request and every byte-range seek request. The player starts on mount when
 * [autoPlay] is set and is paused/torn down on dispose.
 */
@OptIn(ExperimentalComposeUiApi::class, ExperimentalForeignApi::class)
@Composable
actual fun VideoPlayer(
    source: ClipSource,
    modifier: Modifier,
    autoPlay: Boolean,
) {
    val controller = remember(source) {
        val url = NSURL.URLWithString(source.url)
            ?: error("Invalid clip URL: ${source.url}")
        val options: Map<Any?, Any?> = mapOf("AVURLAssetHTTPHeaderFieldsKey" to source.headers)
        val asset = AVURLAsset(uRL = url, options = options)
        val item = AVPlayerItem(asset = asset)
        AVPlayerViewController().apply { player = AVPlayer(playerItem = item) }
    }

    DisposableEffect(controller) {
        if (autoPlay) controller.player?.play()
        onDispose {
            controller.player?.pause()
            controller.player = null
        }
    }

    UIKitViewController(
        factory = { controller },
        modifier = modifier,
    )
}

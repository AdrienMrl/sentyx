package com.sentyx.app.feature.player

import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import com.sentyx.app.data.clip.ClipSource

/**
 * Inline video player for a single event clip. The platform actuals mount a
 * native surface — Media3 ExoPlayer on Android, AVPlayer on iOS — that streams
 * [source]'s URL with its auth headers and supports HTTP range seeking. The
 * player is created when this composable enters composition and released when it
 * leaves; [autoPlay] starts playback immediately on mount.
 *
 * There is no demo actual: demo builds never obtain a non-null [ClipSource] (the
 * loader returns null), so this is only ever composed against a real backend.
 */
@Composable
expect fun VideoPlayer(
    source: ClipSource,
    modifier: Modifier,
    autoPlay: Boolean,
)

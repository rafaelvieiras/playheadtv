// A video url and an audio url played in step, which the `video_player` plugin
// cannot do: it opens one media source per player, and a second one would play
// as silent video. A plugin's `pair` answer is exactly that pair of addresses
// (docs/PLUGINS.md "Modes"), so the app builds this player itself on Media3 and
// merges the two sources. ExoPlayer is the same renderer the ExoPlayer engine
// already uses for one address: platform MediaCodec, the path a TV box's
// hardware decoders are tuned for.
//
// What the merged playback costs the app: the source whose period starts later
// is padded by the merger, and a seek moves both sources (MergingMediaSource
// maps it into each child timeline), which is what keeps a tune-in at 5874s
// playing the audio of that point and not the audio of the file's start.
//
// Everything here runs on the platform thread. ExoPlayer needs a Looper to be
// built on, and every player call arrives from the method channel, which is
// already the platform thread.
package io.github.rafaelvieiras.playheadtv.client

import android.content.Context
import android.os.Handler
import android.os.Looper
import androidx.media3.common.AudioAttributes
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.common.TrackSelectionOverride
import androidx.media3.common.TrackGroup
import androidx.media3.common.Tracks
import androidx.media3.common.VideoSize
import androidx.media3.datasource.DefaultDataSource
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.DefaultMediaSourceFactory
import androidx.media3.exoplayer.source.MergingMediaSource
import io.flutter.plugin.common.BinaryMessenger
import io.flutter.plugin.common.MethodCall
import io.flutter.plugin.common.MethodChannel
import io.flutter.view.TextureRegistry
import java.util.concurrent.ConcurrentHashMap

/**
 * The `playheadtv/paired` channel: one merged player per [Session], each with its
 * own texture the Dart side draws with a `Texture` widget.
 *
 * `create` only answers once the merged timeline is ready (or fails), so the Dart
 * side can await it exactly as it awaits a single-source player's initialize.
 */
class PairedVideos(
    private val context: Context,
    private val textures: TextureRegistry,
    messenger: BinaryMessenger,
) : MethodChannel.MethodCallHandler {

    private val channel = MethodChannel(messenger, CHANNEL)
    private val sessions = ConcurrentHashMap<Int, Session>()
    private var nextHandle = 1

    init {
        channel.setMethodCallHandler(this)
    }

    override fun onMethodCall(call: MethodCall, result: MethodChannel.Result) {
        if (call.method == "create") {
            create(call, result)
            return
        }
        val handle = call.argument<Int>("handle")
        val session = if (handle == null) null else sessions[handle]
        if (session == null) {
            result.error("gone", "no playback with handle $handle", null)
            return
        }
        session.onCall(call, result)
    }

    private fun create(call: MethodCall, result: MethodChannel.Result) {
        val video = call.argument<String>("video")
        val audio = call.argument<String>("audio")
        if (video.isNullOrEmpty() || audio.isNullOrEmpty()) {
            result.error("config", "create needs both a video and an audio url", null)
            return
        }
        val handle = nextHandle++
        val session = Session(handle, video, audio)
        sessions[handle] = session
        session.start(result)
    }

    private fun release(handle: Int) {
        sessions.remove(handle)
    }

    private inner class Session(
        private val handle: Int,
        private val videoUrl: String,
        private val audioUrl: String,
    ) {
        // A SurfaceProducer, not a SurfaceTexture: this Flutter's texture registry
        // hands out a producer whose surface the engine may replace, and the
        // engine presents its frames (the same path the `video_player` plugin
        // takes for its texture players).
        private val producer = textures.createSurfaceProducer()
        private val player: ExoPlayer = ExoPlayer.Builder(context).build()
        private val main = Handler(Looper.getMainLooper())

        private var ready = false
        private var releaseDone = false
        private var ended = false
        private var failure: String? = null
        private var width = 0
        private var height = 0
        private var pending: MethodChannel.Result? = null

        private val ticker = object : Runnable {
            override fun run() {
                if (releaseDone) return
                emit()
                main.postDelayed(this, TICK_MS)
            }
        }

        init {
            producer.setCallback(object : TextureRegistry.SurfaceProducer.Callback {
                override fun onSurfaceAvailable() = bindSurface()

                override fun onSurfaceCleanup() {
                    player.setVideoSurface(null)
                }
            })
            bindSurface()
            // The app plays one thing at a time and fades the volumes of the two
            // crossfade players itself; a player that took audio focus would pause
            // the one fading out.
            player.setAudioAttributes(AudioAttributes.DEFAULT, /* handleAudioFocus= */ false)
            player.addListener(object : Player.Listener {
                override fun onPlaybackStateChanged(state: Int) {
                    when (state) {
                        Player.STATE_READY -> {
                            ready = true
                            answer()
                            sendTracks()
                        }
                        Player.STATE_ENDED -> ended = true
                    }
                    emit()
                }

                override fun onIsPlayingChanged(isPlaying: Boolean) = emit()

                override fun onVideoSizeChanged(size: VideoSize) {
                    width = (size.width * size.pixelWidthHeightRatio).toInt()
                    height = size.height
                    emit()
                }

                override fun onPlayerError(error: PlaybackException) {
                    failure = error.errorCodeName + ": " + (error.message ?: "no description")
                    answer()
                    emit()
                }

                override fun onTracksChanged(tracks: Tracks) {
                    if (ready) sendTracks()
                }
            })
        }

        /** Points the player at the producer's current surface, when it has one. */
        private fun bindSurface() {
            producer.getSurface()?.let { player.setVideoSurface(it) }
        }

        fun start(result: MethodChannel.Result) {
            pending = result
            val factory = DefaultMediaSourceFactory(DefaultDataSource.Factory(context))
            val video = factory.createMediaSource(MediaItem.fromUri(videoUrl))
            val audio = factory.createMediaSource(MediaItem.fromUri(audioUrl))
            player.setMediaSource(MergingMediaSource(video, audio))
            player.prepare()
            main.postDelayed(ticker, TICK_MS)
            main.postDelayed({
                if (!ready && failure == null) {
                    failure = "timed out waiting for the merged timeline"
                    answer()
                    emit()
                }
            }, READY_TIMEOUT_MS)
        }

        /** Answers the pending `create` once, on ready, on failure or on timeout. */
        private fun answer() {
            val result = pending ?: return
            val problem = failure
            if (!ready && problem == null) return
            pending = null
            if (problem != null) {
                result.error("playback", problem, null)
            } else {
                // The tracks travel with the answer: they are known the moment the
                // merged timeline is, and a caller reading them right after an open
                // that returned must not depend on a later message arriving.
                result.success(
                    mapOf(
                        "handle" to handle,
                        "textureId" to producer.id(),
                        "audio" to audioTracks(),
                        "video" to videoTracks(),
                    ),
                )
            }
        }

        fun onCall(call: MethodCall, result: MethodChannel.Result) {
            when (call.method) {
                "play" -> {
                    ended = false
                    player.play()
                    result.success(null)
                }
                "pause" -> {
                    player.pause()
                    result.success(null)
                }
                "seek" -> {
                    val ms = call.argument<Number>("positionMs")?.toLong() ?: 0L
                    ended = false
                    player.seekTo(ms.coerceAtLeast(0L))
                    result.success(null)
                }
                "setVolume" -> {
                    player.volume = (call.argument<Number>("volume")?.toFloat() ?: 1f).coerceIn(0f, 1f)
                    result.success(null)
                }
                "setLooping" -> {
                    player.repeatMode = if (call.argument<Boolean>("looping") == true) {
                        Player.REPEAT_MODE_ONE
                    } else {
                        Player.REPEAT_MODE_OFF
                    }
                    result.success(null)
                }
                "selectAudio" -> {
                    selectAudio(call.argument<List<Number>>("tracks") ?: emptyList())
                    result.success(null)
                }
                "selectSubtitles" -> {
                    // The engine does not expose subtitle tracks (see ExoVideoEngine's
                    // documented limits), so there is nothing to switch here.
                    result.success(null)
                }
                "state" -> {
                    result.success(snapshot())
                }
                "dispose" -> {
                    release()
                    release(handle)
                    result.success(null)
                }
                else -> result.notImplemented()
            }
        }

        /**
         * Plays one audio track (ExoPlayer plays one at a time), addressed the way
         * the app addresses it: the 0-based position within the reported list.
         */
        private fun selectAudio(tracks: List<Number>) {
            val group = audioGroup() ?: return
            val chosen = tracks.firstOrNull()?.toInt()
            val builder = player.trackSelectionParameters.buildUpon()
            if (chosen == null || chosen < 0 || chosen >= group.length) {
                // An empty (or out of range) list means "no preference": the
                // merger's own default, which is the first audio track.
                builder.clearOverridesOfType(C.TRACK_TYPE_AUDIO)
            } else {
                builder.setOverrideForType(TrackSelectionOverride(group, chosen))
            }
            player.trackSelectionParameters = builder.build()
        }

        private fun audioGroup(): TrackGroup? {
            val groups = player.currentTracks.groups
            for (group in groups) {
                if (group.type == C.TRACK_TYPE_AUDIO) return group.mediaTrackGroup
            }
            return null
        }

        private fun sendTracks() {
            post("tracks", mapOf("handle" to handle, "audio" to audioTracks(), "video" to videoTracks()))
        }

        private fun audioTracks(): List<Map<String, Any?>> {
            val out = ArrayList<Map<String, Any?>>()
            for (group in player.currentTracks.groups) {
                if (group.type != C.TRACK_TYPE_AUDIO) continue
                for (i in 0 until group.length) {
                    val format = group.getTrackFormat(i)
                    out.add(
                        mapOf(
                            "language" to format.language,
                            "label" to format.label,
                            "codec" to (format.sampleMimeType ?: ""),
                            "channels" to format.channelCount,
                            "sampleRate" to format.sampleRate,
                            "bitrate" to unsetToZero(format.bitrate),
                        ),
                    )
                }
            }
            return out
        }

        private fun videoTracks(): List<Map<String, Any?>> {
            val out = ArrayList<Map<String, Any?>>()
            for (group in player.currentTracks.groups) {
                if (group.type != C.TRACK_TYPE_VIDEO) continue
                for (i in 0 until group.length) {
                    val format = group.getTrackFormat(i)
                    out.add(
                        mapOf(
                            "codec" to (format.sampleMimeType ?: ""),
                            "width" to format.width,
                            "height" to format.height,
                            "frameRate" to format.frameRate.toDouble(),
                            "bitrate" to unsetToZero(format.bitrate),
                        ),
                    )
                }
            }
            return out
        }

        private fun unsetToZero(value: Int): Int = if (value == C.LENGTH_UNSET.toInt()) 0 else value

        private fun snapshot(): Map<String, Any?> = mapOf(
            "handle" to handle,
            "ready" to ready,
            "position" to player.currentPosition.coerceAtLeast(0L),
            "buffered" to player.bufferedPosition.coerceAtLeast(0L),
            "duration" to if (player.duration == C.TIME_UNSET) 0L else player.duration,
            "playing" to player.isPlaying,
            "buffering" to (player.playbackState == Player.STATE_BUFFERING),
            "ended" to ended,
            "width" to width,
            "height" to height,
            "error" to failure,
        )

        private fun emit() = post("event", snapshot())

        private fun post(method: String, payload: Map<String, Any?>) {
            if (releaseDone) return
            channel.invokeMethod(method, payload)
        }

        private fun release() {
            if (releaseDone) return
            releaseDone = true
            main.removeCallbacksAndMessages(null)
            runCatching {
                player.setVideoSurface(null)
                player.release()
                producer.release()
            }
        }
    }

    private companion object {
        const val CHANNEL = "playheadtv/paired"
        const val TICK_MS = 200L
        const val READY_TIMEOUT_MS = 25_000L
    }
}

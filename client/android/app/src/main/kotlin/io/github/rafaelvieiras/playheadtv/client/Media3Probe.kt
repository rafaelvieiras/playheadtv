// Measures the loudness of what a stream is playing at a given position, using
// the same stack playback uses: ExoPlayer reads the container and the platform
// MediaCodec decodes it, so the PCM measured here is the PCM a viewer hears —
// including Dolby downmix/dialnorm, which a server-side ffmpeg measurement does
// not reproduce.
//
// Why Media3 and not MediaExtractor: MediaExtractor cannot seek over HTTP on
// this device (its sample time stays at 0), so it can only measure the head of
// a file. ExoPlayer seeks by reading the container index with range requests,
// which is how a tune-in at 5874s already plays from that point (see
// PairedVideos.kt). That difference is the whole reason this class exists: the
// interesting measurement is the level of the point being watched, not of the
// file's start.
//
// The measurement taps the AudioSink instead of a processor chain: the sink's
// handleBuffer is exactly "here is decoded PCM, still in the buffer the renderer
// owns", so a duplicate is read and the buffer goes on untouched. The player's
// volume is zeroed, so the probe is silent.
package io.github.rafaelvieiras.playheadtv.client

import android.content.Context
import android.os.Handler
import android.os.HandlerThread
import android.os.Looper
import android.os.SystemClock
import android.util.Log
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.common.Format
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.Renderer
import androidx.media3.exoplayer.RenderersFactory
import androidx.media3.exoplayer.audio.AudioSink
import androidx.media3.exoplayer.audio.DefaultAudioSink
import androidx.media3.exoplayer.audio.ForwardingAudioSink
import androidx.media3.exoplayer.audio.MediaCodecAudioRenderer
import androidx.media3.exoplayer.mediacodec.MediaCodecSelector
import java.nio.ByteBuffer

object Media3Probe {

    private const val TAG = "LoudnessProbe"

    /**
     * Decodes [seconds] of the audio of [url] starting at [startSeconds] and
     * calls [onResult] once, on the main thread, with null on failure.
     *
     * The player lives on its own thread, not the app's main thread. Measured on
     * a device: with the player on the main thread, the "enough audio" signal was
     * posted there and the stop waited for the app to come back, so the probe
     * decoded 27.8 s of a 5 s window (7.5 s of wall clock) while boot was busy on
     * the main thread. On its own thread the stop lands within a buffer, and the
     * measurement never competes with the frame the viewer is watching.
     */
    fun measure(
        context: Context,
        url: String,
        startSeconds: Double,
        seconds: Double,
        timeoutMs: Long,
        onResult: (LoudnessMeter.Measurement?) -> Unit,
    ) {
        val appMain = Handler(Looper.getMainLooper())
        val thread = HandlerThread("playheadtv-probe").apply { start() }
        val probe = Handler(thread.looper)
        val started = SystemClock.elapsedRealtime()
        val sink = MeteringSink(
            DefaultAudioSink.Builder(context).build(),
            seconds,
        )
        var finished = false
        var player: ExoPlayer? = null
        var readyPosition = -1L

        lateinit var finish: (LoudnessMeter.Measurement?) -> Unit
        finish = { result ->
            if (!finished) {
                finished = true
                probe.removeCallbacksAndMessages(null)
                runCatching {
                    player?.stop()
                    player?.release()
                }
                player = null
                thread.quitSafely()
                // One line per measurement, always on: it is the only evidence a
                // device gives of how much was decoded and how long it took, and
                // it is what shows the probe speed is really in effect (a 5 s
                // window at 4x decodes 5 s of audio in well under 5 s of wall
                // time; blocks = decodedSeconds * 2.5).
                if (result == null) {
                    Log.w(TAG, "MEASURED|none|${SystemClock.elapsedRealtime() - started}ms")
                } else {
                    Log.i(
                        TAG,
                        "MEASURED|${fmt(result.lufs)}|${fmt(result.samplePeakDb)}|" +
                            "${result.wallMs}ms|decoded=${fmt(result.decodedSeconds)}s|" +
                            "blocks=${result.blocks}|${result.sampleRate}Hz|${result.channels}ch",
                    )
                }
                appMain.post { onResult(result) }
            }
        }

        sink.onEnough = {
            probe.post {
                val meter = sink.meter
                if (meter == null || meter.completedBlocks == 0) {
                    finish(null)
                } else {
                    finish(
                        LoudnessMeter.Measurement(
                            lufs = meter.integratedLufs(),
                            samplePeakDb = meter.samplePeakDb(),
                            blocks = meter.completedBlocks,
                            decodedSeconds = meter.decodedSeconds,
                            wallMs = SystemClock.elapsedRealtime() - started,
                            mime = sink.mime,
                            sampleRate = sink.sampleRate,
                            channels = sink.channels,
                            firstSampleUs = readyPosition * 1000L,
                        ),
                    )
                }
            }
        }

        // Audio only: no video renderer means the video track is never decoded,
        // and the sink is silent because the player's volume is zero.
        val renderers = RenderersFactory { handler, _, audioListener, _, _ ->
            arrayOf<Renderer>(
                MediaCodecAudioRenderer(
                    context,
                    MediaCodecSelector.DEFAULT,
                    handler,
                    audioListener,
                    sink,
                ),
            )
        }

        probe.post {
            try {
                val exo = ExoPlayer.Builder(context, renderers).setLooper(thread.looper).build()
                player = exo
                exo.volume = 0f
                exo.addListener(object : Player.Listener {
                    override fun onPlaybackStateChanged(state: Int) {
                        if (state == Player.STATE_READY && readyPosition < 0) {
                            readyPosition = exo.currentPosition
                        }
                    }

                    override fun onPlayerError(error: PlaybackException) {
                        Log.w(TAG, "media3 error: ${error.errorCodeName}: ${error.message}", error)
                        finish(null)
                    }
                })
                exo.setMediaItem(MediaItem.fromUri(url))
                exo.prepare()
                if (startSeconds > 0) {
                    exo.seekTo((startSeconds * 1000.0).toLong())
                }
                // Faster than real time on purpose: the meter counts the PCM that
                // leaves the decoder, before the sink's own speed processor gets
                // it, so pulling the audio through faster measures the same
                // content in a fraction of the wall time. A 5 s window measured
                // 3.5 s of wall clock at 1x; this is what makes measuring every
                // bumper affordable.
                exo.setPlaybackSpeed(PROBE_SPEED)
                exo.play()
                probe.postDelayed({ finish(null) }, timeoutMs)
            } catch (e: Exception) {
                Log.w(TAG, "media3 setup failed: ${e.javaClass.simpleName}: ${e.message}", e)
                finish(null)
            }
        }
    }

    /** Runs a probe list from intent extras and logs one line per measurement. */
    fun runFromIntent(
        context: Context,
        jobs: List<Pair<String, Double>>,
        seconds: Double,
        base: String,
        key: String,
    ) {
        Thread {
            for ((id, offset) in jobs) {
                val url = "$base/Videos/$id/stream?Static=true&ApiKey=$key"
                val latch = java.util.concurrent.CountDownLatch(1)
                var line = "FAILED|$id|$offset|no result"
                measure(context, url, offset, seconds, TIMEOUT_MS) { m ->
                    line = if (m == null) {
                        "FAILED|$id|$offset|no result"
                    } else {
                        "RESULT3|$id|$offset|${fmt(m.lufs)}|${fmt(m.samplePeakDb)}|${m.wallMs}|" +
                            "${m.sampleRate}|${m.channels}|${fmt(m.decodedSeconds)}|${m.blocks}|${m.firstSampleUs / 1000}"
                    }
                    latch.countDown()
                }
                latch.await(TIMEOUT_MS + 5000, java.util.concurrent.TimeUnit.MILLISECONDS)
                if (line.startsWith("RESULT3|")) {
                    Log.i(TAG, line)
                } else {
                    Log.w(TAG, line)
                }
            }
            Log.i(TAG, "DONE")
        }.start()
    }

    private fun fmt(v: Double): String = String.format(java.util.Locale.US, "%.2f", v)

    private const val TIMEOUT_MS = 45_000L

    /**
     * How much faster than real time the probe decodes.
     *
     * Only the wall clock changes: the metering sink sees the decoded buffers
     * before the sink applies the speed, and the block count in the log line
     * (47 blocks for a 5 s window) is what proves it on a device.
     */
    private const val PROBE_SPEED = 4.0f

    /**
     * Wraps the real sink and reads every decoded buffer on its way through.
     *
     * A processor would sit in the sink's own chain and had to return exactly
     * the bytes it consumed; the sink boundary carries no such contract, and the
     * buffer stays valid for the delegate.
     */
    private class MeteringSink(
        delegate: AudioSink,
        private val targetSeconds: Double,
    ) : ForwardingAudioSink(delegate) {
        var meter: LoudnessMeter? = null
            private set
        var sampleRate = 0
            private set
        var channels = 0
            private set
        var mime = ""
            private set
        var onEnough: (() -> Unit)? = null

        private var encoding = C.ENCODING_PCM_16BIT
        private var enoughSent = false

        override fun configure(format: Format, specifiedBufferSize: Int, outputChannels: IntArray?) {
            sampleRate = format.sampleRate
            channels = format.channelCount.coerceAtLeast(1)
            mime = format.sampleMimeType ?: ""
            encoding = if (format.pcmEncoding == C.ENCODING_INVALID) {
                C.ENCODING_PCM_16BIT
            } else {
                format.pcmEncoding
            }
            meter = LoudnessMeter(sampleRate, channels)
            super.configure(format, specifiedBufferSize, outputChannels)
        }

        override fun handleBuffer(
            buffer: ByteBuffer,
            presentationTimeUs: Long,
            encodedAccessUnitCount: Int,
        ): Boolean {
            val current = meter
            if (current != null && !enoughSent && buffer.remaining() > 0) {
                // The duplicate consumes nothing: the delegate still gets the
                // whole buffer, in the same position.
                current.feed(buffer.duplicate(), encoding)
                if (current.decodedSeconds >= targetSeconds) {
                    // Stop counting at the window that was asked for: the sink
                    // keeps being fed until the player is actually stopped, and
                    // whatever arrives after this is not part of the measurement.
                    enoughSent = true
                    onEnough?.invoke()
                }
            }
            return super.handleBuffer(buffer, presentationTimeUs, encodedAccessUnitCount)
        }
    }
}

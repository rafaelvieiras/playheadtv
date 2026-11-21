package io.github.rafaelvieiras.playheadtv.client

import android.content.Context
import android.media.MediaCodec
import android.media.MediaExtractor
import android.media.MediaFormat
import android.os.SystemClock
import android.util.Log
import java.nio.ByteBuffer
import java.nio.ByteOrder
import kotlin.math.PI
import kotlin.math.abs
import kotlin.math.log10
import kotlin.math.pow
import kotlin.math.tan

/**
 * Measures loudness of a piece of a stream without playing it.
 *
 * This exists to answer one question with numbers instead of guesses: can the
 * client measure a remote item's loudness by itself, fast enough to finish
 * inside the preload lead, so that no backend work is needed to level the
 * channels? It decodes with the platform decoders (the box declares ac3, eac3,
 * dtshd, flac, opus and vorbis), reimplements ITU-R BS.1770 K-weighting and
 * gating over 400 ms blocks, and reports integrated loudness plus sample peak.
 *
 * It is validation code: driven by intent extras, it logs one line per
 * measurement and never touches playback.
 */
object LoudnessProbe {

    private const val TAG = "LoudnessProbe"

    /**
     * Decodes [seconds] of the audio track of [url] starting at [offsetSeconds]
     * and returns its integrated loudness. Runs on the calling thread.
     */
    fun measure(url: String, offsetSeconds: Double, seconds: Double): LoudnessMeter.Measurement {
        val wallStart = SystemClock.elapsedRealtime()
        val extractor = MediaExtractor()
        try {
            extractor.setDataSource(url, null)
        } catch (e: Exception) {
            throw IllegalStateException("setDataSource failed: ${e.message}", e)
        }

        var audioTrack = -1
        var inputFormat: MediaFormat? = null
        for (i in 0 until extractor.trackCount) {
            val f = extractor.getTrackFormat(i)
            val mime = f.getString(MediaFormat.KEY_MIME) ?: continue
            if (mime.startsWith("audio/")) {
                audioTrack = i
                inputFormat = f
                break
            }
        }
        if (audioTrack < 0 || inputFormat == null) {
            throw IllegalStateException("no audio track")
        }
        extractor.selectTrack(audioTrack)
        if (offsetSeconds > 0) {
            extractor.seekTo((offsetSeconds * 1_000_000).toLong(), MediaExtractor.SEEK_TO_PREVIOUS_SYNC)
        }
        val mime = inputFormat.getString(MediaFormat.KEY_MIME) ?: "audio/unknown"
        // After a seek on an http source, sampleTime is where decoding really
        // starts: if it stays at 0 for every offset, seeking is not working and
        // every "window" is the head of the file.
        val firstSampleUs = extractor.sampleTime

        val codec = MediaCodec.createDecoderByType(mime)
        codec.configure(inputFormat, null, null, 0)
        codec.start()

        // getInteger(name, default) is API 29+; this box runs Android 9 (API 28).
        var sampleRate = intOr(inputFormat, MediaFormat.KEY_SAMPLE_RATE, 48_000)
        var channelCount = intOr(inputFormat, MediaFormat.KEY_CHANNEL_COUNT, 2)
        var pcmEncoding = android.media.AudioFormat.ENCODING_PCM_16BIT

        val wantedSamples = (seconds * sampleRate).toLong()
        var decodedSamples = 0L
        var blockFrames = 0
        var blocks = 0
        var meter: LoudnessMeter? = null

        val info = MediaCodec.BufferInfo()
        var sawInputEos = false
        var sawOutputEos = false
        var guard = 0

        while (!sawOutputEos && decodedSamples < wantedSamples && guard < 1_000_000) {
            guard++
            if (!sawInputEos) {
                val inIndex = codec.dequeueInputBuffer(10_000)
                if (inIndex >= 0) {
                    val buffer: ByteBuffer = codec.getInputBuffer(inIndex)!!
                    val size = extractor.readSampleData(buffer, 0)
                    if (size < 0) {
                        codec.queueInputBuffer(inIndex, 0, 0, 0, MediaCodec.BUFFER_FLAG_END_OF_STREAM)
                        sawInputEos = true
                    } else {
                        codec.queueInputBuffer(inIndex, 0, size, extractor.sampleTime, 0)
                        extractor.advance()
                    }
                }
            }

            when (val outIndex = codec.dequeueOutputBuffer(info, 10_000)) {
                MediaCodec.INFO_OUTPUT_FORMAT_CHANGED -> {
                    val out = codec.outputFormat
                    sampleRate = intOr(out, MediaFormat.KEY_SAMPLE_RATE, sampleRate)
                    channelCount = intOr(out, MediaFormat.KEY_CHANNEL_COUNT, channelCount)
                    pcmEncoding = if (out.containsKey(MediaFormat.KEY_PCM_ENCODING)) {
                        out.getInteger(MediaFormat.KEY_PCM_ENCODING)
                    } else {
                        android.media.AudioFormat.ENCODING_PCM_16BIT
                    }
                    meter = LoudnessMeter(sampleRate, channelCount)
                    blockFrames = 0
                }
                MediaCodec.INFO_TRY_AGAIN_LATER -> Unit
                MediaCodec.INFO_OUTPUT_BUFFERS_CHANGED -> Unit
                else -> {
                    if (outIndex >= 0) {
                        val buf = codec.getOutputBuffer(outIndex)
                        val m = meter ?: LoudnessMeter(sampleRate, channelCount).also { meter = it }
                        if (buf != null && info.size > 0) {
                            buf.position(info.offset)
                            buf.limit(info.offset + info.size)
                            val frames = m.feed(buf, pcmEncoding)
                            decodedSamples += frames
                            blockFrames += frames.toInt()
                            if (blockFrames >= m.blockFrames) {
                                blocks++
                                blockFrames = 0
                            }
                        }
                        codec.releaseOutputBuffer(outIndex, false)
                        if (info.flags and MediaCodec.BUFFER_FLAG_END_OF_STREAM != 0) {
                            sawOutputEos = true
                        }
                    }
                }
            }
        }

        codec.stop()
        codec.release()
        extractor.release()

        val m = meter ?: throw IllegalStateException("decoder never produced PCM")
        val lufs = m.integratedLufs()
        return LoudnessMeter.Measurement(
            lufs = lufs,
            samplePeakDb = m.samplePeakDb(),
            blocks = m.completedBlocks,
            decodedSeconds = decodedSamples.toDouble() / sampleRate,
            wallMs = SystemClock.elapsedRealtime() - wallStart,
            mime = mime,
            sampleRate = sampleRate,
            channels = channelCount,
            firstSampleUs = firstSampleUs,
        )
    }

    /**
     * Runs a probe list from intent extras and logs one line per measurement.
     *
     * `probe_ids` is a comma separated list of library item ids, `probe_offsets`
     * a comma separated list of offsets in seconds applied to every id,
     * `probe_seconds` the window length, `probe_base` and `probe_key` the server
     * and its API key.
     */
    fun runFromIntent(context: Context, jobs: List<Pair<String, Double>>, seconds: Double, base: String, key: String) {
        Thread {
            for ((id, offset) in jobs) {
                val url = "$base/Videos/$id/stream?Static=true&ApiKey=$key"
                try {
                    val m = measure(url, offset, seconds)
                    Log.i(
                        TAG,
                        "RESULT|$id|$offset|${fmt(m.lufs)}|${fmt(m.samplePeakDb)}|${m.wallMs}|" +
                            "${m.mime}|${m.sampleRate}|${m.channels}|${fmt(m.decodedSeconds)}|${m.blocks}|${m.firstSampleUs}",
                    )
                } catch (e: Exception) {
                    Log.w(TAG, "FAILED|$id|$offset|${e.javaClass.simpleName}: ${e.message}")
                }
            }
            Log.i(TAG, "DONE")
        }.start()
    }

    private fun fmt(v: Double): String = String.format(java.util.Locale.US, "%.2f", v)

    private fun intOr(format: MediaFormat, key: String, fallback: Int): Int =
        if (format.containsKey(key)) format.getInteger(key) else fallback

}

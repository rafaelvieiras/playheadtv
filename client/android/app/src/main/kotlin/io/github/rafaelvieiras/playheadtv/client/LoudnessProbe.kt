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

    /** One measurement: integrated loudness (LUFS) and sample peak (dBFS). */
    data class Measurement(
        val lufs: Double,
        val samplePeakDb: Double,
        val blocks: Int,
        val decodedSeconds: Double,
        val wallMs: Long,
        val mime: String,
        val sampleRate: Int,
        val channels: Int,
        val firstSampleUs: Long,
    )

    /**
     * Decodes [seconds] of the audio track of [url] starting at [offsetSeconds]
     * and returns its integrated loudness. Runs on the calling thread.
     */
    fun measure(url: String, offsetSeconds: Double, seconds: Double): Measurement {
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
        var meter: KWeightedMeter? = null

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
                    meter = KWeightedMeter(sampleRate, channelCount)
                    blockFrames = 0
                }
                MediaCodec.INFO_TRY_AGAIN_LATER -> Unit
                MediaCodec.INFO_OUTPUT_BUFFERS_CHANGED -> Unit
                else -> {
                    if (outIndex >= 0) {
                        val buf = codec.getOutputBuffer(outIndex)
                        val m = meter ?: KWeightedMeter(sampleRate, channelCount).also { meter = it }
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
        return Measurement(
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
     * BS.1770 K-weighting plus gated block loudness, streaming: samples go in as
     * the decoder hands them over, no full-file buffer.
     */
    private class KWeightedMeter(sampleRate: Int, private val channels: Int) {
        private val shelf = Biquad.highShelf(SHELF_F0, SHELF_GAIN_DB, SHELF_Q, sampleRate)
        private val highPass = Biquad.highPass(HP_F0, HP_Q, sampleRate)
        private val z1 = DoubleArray(channels)
        private val z2 = DoubleArray(channels)
        private val y1 = DoubleArray(channels)
        private val y2 = DoubleArray(channels)

        // 400 ms blocks with 75% overlap: keep the last 3 hops so each new hop
        // completes one block without buffering the whole window.
        val blockFrames = (0.4 * sampleRate).toInt().coerceAtLeast(1)
        private val hop = (blockFrames / 4).coerceAtLeast(1)
        private val hopEnergy = DoubleArray(channels)
        private val history = Array(4) { DoubleArray(channels) }
        private var framesSinceHop = 0
        private var hopCount = 0
        private var peak = 0.0
        private val blockLoudness = ArrayList<Double>()
        var completedBlocks = 0
            private set

        private val weights = channelWeights(channels)

        /** Returns how many frames were consumed. */
        fun feed(buffer: ByteBuffer, encoding: Int): Long {
            val order = if (buffer.order() == ByteOrder.BIG_ENDIAN) ByteOrder.BIG_ENDIAN else ByteOrder.LITTLE_ENDIAN
            buffer.order(order)
            val bytesPerSample = if (encoding == android.media.AudioFormat.ENCODING_PCM_FLOAT) 4 else 2
            val total = buffer.remaining() / bytesPerSample
            val frames = total / channels
            for (f in 0 until frames) {
                for (c in 0 until channels) {
                    val raw = if (bytesPerSample == 4) {
                        buffer.getFloat().toDouble()
                    } else {
                        buffer.getShort().toDouble() / 32768.0
                    }
                    if (abs(raw) > peak) peak = abs(raw)
                    // K-weighting is applied per channel: two biquads in series.
                    val s1 = shelf.step(raw, c, z1, z2)
                    val y = highPass.step(s1, c, y1, y2)
                    hopEnergy[c] += y * y
                }
                // A decoder buffer boundary must not restart the hop clock:
                // feed() is called once per output buffer, of arbitrary length.
                framesSinceHop++
                if (framesSinceHop >= hop) {
                    framesSinceHop = 0
                    finishHop()
                }
            }
            return frames.toLong()
        }

        private fun finishHop() {
            // Slide the 4-hop window: block energy is the mean over 400 ms.
            for (c in 0 until channels) {
                history[hopCount % 4][c] = hopEnergy[c] / hop
                hopEnergy[c] = 0.0
            }
            hopCount++
            if (hopCount >= 4) {
                var sum = 0.0
                for (h in 0 until 4) {
                    for (c in 0 until channels) {
                        sum += weights[c] * history[h][c]
                    }
                }
                val blockEnergy = sum / 4.0
                if (blockEnergy > 0) {
                    blockLoudness.add(LOUDNESS_OFFSET + 10.0 * log10(blockEnergy))
                    completedBlocks++
                }
            }
        }

        fun integratedLufs(): Double {
            if (blockLoudness.isEmpty()) return Double.NEGATIVE_INFINITY
            // Absolute gate at -70 LUFS, then a relative gate 10 LU below the
            // mean of what survived.
            val absolute = blockLoudness.filter { it > ABSOLUTE_GATE_LUFS }
            if (absolute.isEmpty()) return Double.NEGATIVE_INFINITY
            val mean = energyMean(absolute)
            val relativeGate = mean - RELATIVE_GATE_LU
            val gated = absolute.filter { it > relativeGate }
            return if (gated.isEmpty()) mean else energyMean(gated)
        }

        private fun energyMean(values: List<Double>): Double {
            var total = 0.0
            for (v in values) total += 10.0.pow((v - LOUDNESS_OFFSET) / 10.0)
            return LOUDNESS_OFFSET + 10.0 * log10(total / values.size)
        }

        fun samplePeakDb(): Double = if (peak <= 0.0) -999.0 else 20.0 * log10(peak)

        private fun channelWeights(n: Int): DoubleArray = when (n) {
            1 -> doubleArrayOf(1.0)
            2 -> doubleArrayOf(1.0, 1.0)
            6 -> doubleArrayOf(1.0, 1.0, 1.0, 0.0, 1.41, 1.41)
            else -> DoubleArray(n) { if (it == 3) 0.0 else if (it >= 4) 1.41 else 1.0 }
        }
    }

    /** Direct-form II transposed biquad with per-channel state. */
    private class Biquad(
        private val b0: Double,
        private val b1: Double,
        private val b2: Double,
        private val a1: Double,
        private val a2: Double,
    ) {
        fun step(x: Double, c: Int, z1: DoubleArray, z2: DoubleArray): Double {
            val y = b0 * x + z1[c]
            z1[c] = b1 * x - a1 * y + z2[c]
            z2[c] = b2 * x - a2 * y
            return y
        }

        companion object {
            /** BS.1770 stage 1 (head shelf), coefficients derived for [fs]. */
            fun highShelf(f0: Double, gainDb: Double, q: Double, fs: Int): Biquad {
                val k = tan(PI * f0 / fs)
                val vh = 10.0.pow(gainDb / 20.0)
                val vb = vh.pow(0.4996667741545416)
                val a0 = 1.0 + k / q + k * k
                return Biquad(
                    b0 = (vh + vb * k / q + k * k) / a0,
                    b1 = 2.0 * (k * k - vh) / a0,
                    b2 = (vh - vb * k / q + k * k) / a0,
                    a1 = 2.0 * (k * k - 1.0) / a0,
                    a2 = (1.0 - k / q + k * k) / a0,
                )
            }

            /** BS.1770 stage 2 (RLB high pass). */
            fun highPass(f0: Double, q: Double, fs: Int): Biquad {
                val k = tan(PI * f0 / fs)
                val a0 = 1.0 + k / q + k * k
                return Biquad(
                    b0 = 1.0 / a0,
                    b1 = -2.0 / a0,
                    b2 = 1.0 / a0,
                    a1 = 2.0 * (k * k - 1.0) / a0,
                    a2 = (1.0 - k / q + k * k) / a0,
                )
            }
        }
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

    // BS.1770 constants: head shelf +4 dB at ~1.7 kHz, RLB high pass at 38 Hz.
    private const val SHELF_F0 = 1681.974450955533
    private const val SHELF_GAIN_DB = 3.999843853973347
    private const val SHELF_Q = 0.7071752369554196
    private const val HP_F0 = 38.13547087602444
    private const val HP_Q = 0.5003270373238773
    private const val LOUDNESS_OFFSET = -0.691
    private const val ABSOLUTE_GATE_LUFS = -70.0
    private const val RELATIVE_GATE_LU = -10.0
}

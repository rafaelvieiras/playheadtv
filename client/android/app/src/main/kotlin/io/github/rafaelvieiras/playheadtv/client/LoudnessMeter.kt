package io.github.rafaelvieiras.playheadtv.client

import java.nio.ByteBuffer
import java.nio.ByteOrder
import kotlin.math.PI
import kotlin.math.abs
import kotlin.math.log10
import kotlin.math.pow
import kotlin.math.tan

/**
 * ITU-R BS.1770 loudness of a stream of decoded PCM, K-weighting plus gating.
 *
 * Streaming on purpose: samples go in as a decoder or an audio pipeline hands
 * them over, so nothing has to be buffered for the whole window. Both probes
 * share it — the `MediaExtractor` one, which decodes a file's head, and the
 * Media3 one, which decodes where playback actually is.
 */
class LoudnessMeter(sampleRate: Int, private val channels: Int) {

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

    private var framesTotal = 0L
    private val sampleRateValue = sampleRate

    private val weights = channelWeights(channels)

    /** Frames consumed so far, as seconds of audio. */
    val decodedSeconds: Double
        get() = if (sampleRateValue <= 0) 0.0 else framesTotal.toDouble() / sampleRateValue

    /** Returns how many frames were consumed. */
    fun feed(buffer: ByteBuffer, encoding: Int): Long {
        // Never trust the buffer's current order: a ByteBuffer that nobody
        // touched reports BIG_ENDIAN, and PCM is little endian, so preserving it
        // reads every sample byte-swapped.
        val order = ByteOrder.LITTLE_ENDIAN
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
            // A decoder buffer boundary must not restart the hop clock: feed() is
            // called once per buffer, of arbitrary length.
            framesSinceHop++
            if (framesSinceHop >= hop) {
                framesSinceHop = 0
                finishHop()
            }
        }
        framesTotal += frames
        return frames.toLong()
    }

    /** Feeds interleaved 16-bit samples already copied out of a buffer. */
    fun feed(shorts: ShortArray, frames: Int) {
        for (f in 0 until frames) {
            for (c in 0 until channels) {
                val raw = shorts[f * channels + c].toDouble() / 32768.0
                if (abs(raw) > peak) peak = abs(raw)
                val s1 = shelf.step(raw, c, z1, z2)
                val y = highPass.step(s1, c, y1, y2)
                hopEnergy[c] += y * y
            }
            framesSinceHop++
            if (framesSinceHop >= hop) {
                framesSinceHop = 0
                finishHop()
            }
        }
        framesTotal += frames
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
        // Absolute gate at -70 LUFS, then a relative gate 10 LU below the mean of
        // what survived.
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

    companion object {
        // BS.1770 constants: head shelf +4 dB at ~1.7 kHz, RLB high pass at 38 Hz.
        const val SHELF_F0 = 1681.974450955533
        const val SHELF_GAIN_DB = 3.999843853973347
        const val SHELF_Q = 0.7071752369554196
        const val HP_F0 = 38.13547087602444
        const val HP_Q = 0.5003270373238773
        const val LOUDNESS_OFFSET = -0.691
        const val ABSOLUTE_GATE_LUFS = -70.0
        const val RELATIVE_GATE_LU = -10.0
    }
}

// The `playheadtv/loudness` channel: `measure` answers with the loudness of a
// piece of a stream (see Media3Probe.kt for how it decodes), and `setPreamp`
// sets the global gain above unity that the player's own volume cannot express
// (see AudioPreamp.kt).
//
// Answering with a null map means "no opinion" — no audio track, or the timeout
// fired. The Dart side then keeps whatever gain it was using, which is why a
// measurement that fails never mutes a channel.
package io.github.rafaelvieiras.playheadtv.client

import android.content.Context
import io.flutter.plugin.common.BinaryMessenger
import io.flutter.plugin.common.MethodCall
import io.flutter.plugin.common.MethodChannel

class LoudnessChannel(
    private val context: Context,
    messenger: BinaryMessenger,
) : MethodChannel.MethodCallHandler {

    private val channel = MethodChannel(messenger, CHANNEL)

    init {
        channel.setMethodCallHandler(this)
    }

    override fun onMethodCall(call: MethodCall, result: MethodChannel.Result) {
        if (call.method == "setPreamp") {
            val db = (call.argument<Number>("db") ?: 0).toDouble()
            result.success(AudioPreamp.set(db))
            return
        }
        if (call.method == "preampState") {
            result.success(AudioPreamp.state())
            return
        }
        if (call.method != "measure") {
            result.notImplemented()
            return
        }
        val url = call.argument<String>("url")
        if (url.isNullOrEmpty()) {
            result.error("config", "measure needs a url", null)
            return
        }
        val startSeconds = (call.argument<Number>("startSeconds") ?: 0).toDouble()
        val seconds = (call.argument<Number>("seconds") ?: 5).toDouble()
        var answered = false
        Media3Probe.measure(context, url, startSeconds, seconds, TIMEOUT_MS) { m ->
            if (answered) return@measure
            answered = true
            if (m == null) {
                result.success(null)
            } else {
                result.success(
                    mapOf(
                        "lufs" to m.lufs,
                        "peakDb" to m.samplePeakDb,
                        "seconds" to m.decodedSeconds,
                        "blocks" to m.blocks,
                    ),
                )
            }
        }
    }

    private companion object {
        const val CHANNEL = "playheadtv/loudness"
        const val TIMEOUT_MS = 30_000L
    }
}

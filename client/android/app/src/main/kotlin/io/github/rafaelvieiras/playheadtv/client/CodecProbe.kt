package io.github.rafaelvieiras.playheadtv.client

import android.media.MediaCodecList
import android.media.MediaFormat
import io.flutter.plugin.common.MethodCall
import io.flutter.plugin.common.MethodChannel

/**
 * Answers "can this device decode that?" without opening anything.
 *
 * It reads the platform's decoder list (`MediaCodecList`), which is a registry
 * lookup: no codec instance, no surface, no buffers, nothing that a player would
 * have to clean up. Creating a real decoder to test would be worse than the
 * failure it tries to prevent - it costs an instance of the scarcest resource on
 * a set-top box.
 *
 * The codec names are ffprobe's (what the media library stores), mapped to the
 * MIME types Android knows. A name this table does not know answers `unknown`,
 * which the caller treats as "try it", never as a refusal.
 */
class CodecProbe : MethodChannel.MethodCallHandler {
    constructor(messenger: io.flutter.plugin.common.BinaryMessenger) {
        MethodChannel(messenger, CHANNEL).setMethodCallHandler(this)
    }

    override fun onMethodCall(call: MethodCall, result: MethodChannel.Result) {
        if (call.method != "canDecode") {
            result.notImplemented()
            return
        }
        val codec = (call.argument<String>("codec") ?: "").lowercase()
        val width = call.argument<Int>("width") ?: 0
        val height = call.argument<Int>("height") ?: 0
        val mime = MIME_BY_CODEC[codec]
        if (mime == null) {
            result.success("unknown")
            return
        }
        result.success(if (hasDecoder(mime, width, height)) "supported" else "unsupported")
    }

    private fun hasDecoder(mime: String, width: Int, height: Int): Boolean {
        val key = "$mime|${width}x$height"
        cache[key]?.let { return it }
        val format = MediaFormat.createVideoFormat(mime, if (width > 0) width else 1920, if (height > 0) height else 1080)
        val found = runCatching {
            // The list is built lazily by the framework and then reused, so this
            // does not walk every codec on every call.
            MediaCodecList(MediaCodecList.REGULAR_CODECS).findDecoderForFormat(format) != null
        }.getOrDefault(false)
        cache[key] = found
        return found
    }

    private val cache = mutableMapOf<String, Boolean>()

    companion object {
        private const val CHANNEL = "playheadtv/codec"

        /**
         * ffprobe codec name -> Android MIME. Only what Android can name: a
         * container the platform has no decoder for (RealVideo, WMV) is absent
         * on purpose, so it answers `unknown` and the attempt happens the way it
         * always did instead of being refused on a guess.
         */
        private val MIME_BY_CODEC = mapOf(
            "h264" to MediaFormat.MIMETYPE_VIDEO_AVC,
            "hevc" to MediaFormat.MIMETYPE_VIDEO_HEVC,
            "mpeg4" to MediaFormat.MIMETYPE_VIDEO_MPEG4,
            "mpeg2video" to MediaFormat.MIMETYPE_VIDEO_MPEG2,
            "mpeg1video" to MediaFormat.MIMETYPE_VIDEO_MPEG2,
            "vp8" to MediaFormat.MIMETYPE_VIDEO_VP8,
            "vp9" to MediaFormat.MIMETYPE_VIDEO_VP9,
            // Literal, not MediaFormat.MIMETYPE_VIDEO_AV1: that constant arrived
            // in API 29 and this app runs on older set-top boxes.
            "av1" to "video/av01",
            "h263" to MediaFormat.MIMETYPE_VIDEO_H263,
        )
    }
}

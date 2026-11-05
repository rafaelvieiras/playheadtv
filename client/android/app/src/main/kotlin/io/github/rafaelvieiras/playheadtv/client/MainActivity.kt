package io.github.rafaelvieiras.playheadtv.client

import android.app.UiModeManager
import android.content.res.Configuration
import android.os.Build
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel

class MainActivity : FlutterActivity() {
    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        // Tells the pairing flow whether this is a TV and what to call it: Dart
        // cannot tell Android TV from a phone, and the hostname is just "localhost".
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "playheadtv/device")
            .setMethodCallHandler { call, result ->
                if (call.method == "info") {
                    val ui = getSystemService(UI_MODE_SERVICE) as? UiModeManager
                    val tv = ui?.currentModeType == Configuration.UI_MODE_TYPE_TELEVISION
                    result.success(mapOf("tv" to tv, "model" to (Build.MODEL ?: "")))
                } else {
                    result.notImplemented()
                }
            }
        // A plugin's `pair` answer is two addresses; the `video_player` plugin
        // opens one per player, so the app builds that player here.
        PairedVideos(applicationContext, flutterEngine.renderer, flutterEngine.dartExecutor.binaryMessenger)
    }
}

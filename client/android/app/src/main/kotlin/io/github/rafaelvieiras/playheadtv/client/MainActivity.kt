package io.github.rafaelvieiras.playheadtv.client

import android.app.UiModeManager
import android.content.Intent
import android.content.pm.PackageManager
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
                    // Two independent signals: the UI mode, which reports how the
                    // system wants apps presented, and the package features, which
                    // is how a store classifies the device. A box whose UI mode
                    // reads as something else still says leanback or television,
                    // and getting this wrong hands a television the phone layout.
                    val tv = ui?.currentModeType == Configuration.UI_MODE_TYPE_TELEVISION ||
                        packageManager.hasSystemFeature(PackageManager.FEATURE_LEANBACK) ||
                        packageManager.hasSystemFeature(PackageManager.FEATURE_TELEVISION)
                    result.success(mapOf("tv" to tv, "model" to (Build.MODEL ?: "")))
                } else {
                    result.notImplemented()
                }
            }
        // Leveling: measures how loud a piece of a stream is, on the device,
        // with the same decoders playback uses (see LoudnessChannel.kt).
        LoudnessChannel(applicationContext, flutterEngine.dartExecutor.binaryMessenger)
        // A plugin's `pair` answer is two addresses; the `video_player` plugin
        // opens one per player, so the app builds that player here.
        PairedVideos(applicationContext, flutterEngine.renderer, flutterEngine.dartExecutor.binaryMessenger)
    }

    override fun onCreate(savedInstanceState: android.os.Bundle?) {
        super.onCreate(savedInstanceState)
        maybeRunLoudnessProbe(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        maybeRunLoudnessProbe(intent)
    }

    /**
     * Validation hook for the loudness work: `am start ... --es probe_jobs`
     * measures the given items without playing them and logs the result. The
     * app behaves exactly as before when the extras are absent.
     *
     * `probe_mode` picks the reader: `media3` decodes where playback is (it can
     * seek, so `offset` means the point being watched), anything else decodes
     * the head of the file with MediaExtractor.
     *
     * The server and its key come from the settings this app already stored, so
     * no credential ever travels through a command line.
     */
    private fun maybeRunLoudnessProbe(intent: Intent?) {
        // `probe_jobs` is `id:offset;id:offset;...` so a job can never be paired
        // with the wrong offset.
        val jobs = intent?.getStringExtra("probe_jobs")
            ?.split(";")
            ?.mapNotNull { entry ->
                val i = entry.indexOf(':')
                if (i <= 0) return@mapNotNull null
                val off = entry.substring(i + 1).toDoubleOrNull() ?: return@mapNotNull null
                entry.substring(0, i) to off
            }
            ?.filter { it.first.isNotBlank() }
            ?: return
        val prefs = getSharedPreferences("FlutterSharedPreferences", MODE_PRIVATE)
        // SettingsService stores the connection under `connection_<key>`.
        val base = intent.getStringExtra("probe_base")
            ?: prefs.getString("flutter.connection_jellyfin_url", null)
            ?: prefs.getString("flutter.jellyfin_url", null)
            ?: return
        val key = intent.getStringExtra("probe_key")
            ?: prefs.getString("flutter.connection_jellyfin_api_key", null)
            ?: prefs.getString("flutter.jellyfin_api_key", null)
            ?: return
        val seconds = intent.getStringExtra("probe_seconds")?.toDoubleOrNull() ?: 30.0
        val mode = intent.getStringExtra("probe_mode") ?: "extractor"
        if (mode == "media3") {
            Media3Probe.runFromIntent(applicationContext, jobs, seconds, base.trimEnd('/'), key)
        } else {
            LoudnessProbe.runFromIntent(applicationContext, jobs, seconds, base.trimEnd('/'), key)
        }
    }
}

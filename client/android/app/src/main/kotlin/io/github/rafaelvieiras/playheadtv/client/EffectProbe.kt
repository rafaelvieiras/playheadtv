// Phase 0 of the leveling work: does a platform audio effect on the player's
// session actually change the level that leaves the device?
//
// A gain above 1 is the one thing Player.setVolume cannot do, so the cheaper
// path to boosting quiet material is an AudioEffect on the session the player
// owns. The framework effect is preferred-but-not-guaranteed: on a device whose
// audio HAL does not implement it, attaching succeeds and nothing changes. This
// probe exists to tell those two cases apart with numbers instead of ears.
//
// It plays for real (volume 1.0, no metering tap), so the caller can capture the
// output of the device and compare it with the same run at gain 0.
package io.github.rafaelvieiras.playheadtv.client

import android.content.Context
import android.media.AudioManager
import android.media.audiofx.LoudnessEnhancer
import android.os.Handler
import android.os.Looper
import android.util.Log
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.exoplayer.ExoPlayer

object EffectProbe {

    private const val TAG = "LoudnessProbe"

    /**
     * Plays [url] at full volume for [seconds] with a [gainMb] gain (millibels,
     * 1200 = +12 dB) applied through a [LoudnessEnhancer] on the session.
     *
     * The session id is generated before the player is built and handed to it,
     * so the effect is attached to a session that already exists: attaching to a
     * session that has no track does nothing, and a track created without an
     * effect does not pick one up later.
     */
    fun run(context: Context, url: String, gainMb: Int, seconds: Double, global: Boolean = false) {
        val main = Handler(Looper.getMainLooper())
        main.post {
            val player: ExoPlayer
            val fx: LoudnessEnhancer
            val session: Int
            try {
                val audio = context.getSystemService(Context.AUDIO_SERVICE) as AudioManager
                // Session 0 is the output mix, not a player session. Later
                // Android versions restrict effects there for unprivileged apps,
                // so whether hasControl() says yes is the whole answer.
                session = if (global) 0 else audio.generateAudioSessionId()
                player = ExoPlayer.Builder(context).build()
                if (!global) player.setAudioSessionId(session)
                player.volume = 1f
                fx = LoudnessEnhancer(session)
                fx.setTargetGain(gainMb)
                fx.setEnabled(true)
                Log.i(
                    TAG,
                    "EFFECT|setup|global=$global|session=$session|asked=$gainMb|target=${fx.getTargetGain()}|" +
                        "enabled=${fx.getEnabled()}|control=${fx.hasControl()}",
                )
            } catch (e: Throwable) {
                // The effect is preferred-but-not-guaranteed, so a failure here is
                // a result, not a crash: it says the cheap path is closed.
                Log.e(TAG, "EFFECT|failed|${e.javaClass.simpleName}: ${e.message}", e)
                Log.i(TAG, "EFFECT|DONE")
                return@post
            }

            player.addListener(object : Player.Listener {
                override fun onPlaybackStateChanged(state: Int) {
                    Log.i(TAG, "EFFECT|state=$state|pos=${player.currentPosition}")
                }

                override fun onPlayerError(error: PlaybackException) {
                    Log.w(TAG, "EFFECT|error|${error.errorCodeName}: ${error.message}")
                }
            })

            player.setMediaItem(MediaItem.fromUri(url))
            player.prepare()
            player.playWhenReady = true

            main.postDelayed({
                Log.i(TAG, "EFFECT|end|pos=${player.currentPosition}|enabled=${fx.getEnabled()}")
                runCatching { player.stop() }
                runCatching { player.release() }
                runCatching { fx.release() }
                Log.i(TAG, "EFFECT|DONE")
            }, (seconds * 1000.0).toLong())
        }
    }
}

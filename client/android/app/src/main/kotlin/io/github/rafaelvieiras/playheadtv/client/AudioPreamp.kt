// The global pre-amp: a LoudnessEnhancer on the output mix (audio session 0).
//
// `Player.setVolume` cannot go above 1, so the only way to bring quiet material
// up to the level of the rest is gain above unity. Attaching the framework
// effect to the mix instead of to a player session keeps that gain available
// without patching the video player plugin: the player keeps doing plain 0..1
// volume for attenuation, and this object lifts everything by a fixed amount.
//
// Measured before it was written: on API 36 and on the Mi Box (API 28) the
// effect takes control, applies the requested gain exactly (+6 -> +6.00 dB,
// +40 -> +39.90 dB) and has a limiter, so peaks pushed past full scale compress
// instead of clipping. When the platform refuses the effect, `control` comes
// back false and the caller just keeps playing unleveled audio.
package io.github.rafaelvieiras.playheadtv.client

import android.media.audiofx.LoudnessEnhancer
import android.util.Log

object AudioPreamp {

    private const val TAG = "LoudnessProbe"

    // Session 0 is the output mix, not a player session.
    private const val GLOBAL_SESSION = 0

    // The framework clamps this anyway; keeping the ceiling here means the
    // answer we return describes what was asked for.
    private const val MAX_MB = 2_000

    private var fx: LoudnessEnhancer? = null
    private var targetMb = 0

    /**
     * Applies [db] of gain to everything this app plays and reports what the
     * platform actually did. Never throws: a platform that refuses the effect is
     * an answer, not a crash.
     */
    @Synchronized
    fun set(db: Double): Map<String, Any?> {
        targetMb = (db * 100).toInt().coerceIn(0, MAX_MB)
        return apply()
    }

    @Synchronized
    fun state(): Map<String, Any?> = describe()

    private fun apply(): Map<String, Any?> {
        var effect = fx
        if (effect == null || !effect.hasControl()) {
            effect?.release()
            effect = try {
                LoudnessEnhancer(GLOBAL_SESSION).also { created ->
                    // A route change (HDMI handshake, device switch) can drop
                    // control; without this the pre-amp would silently go away
                    // for the rest of the session.
                    created.setControlStatusListener { _, control ->
                        Log.i(TAG, "PREAMP|control=$control")
                        if (!control) synchronized(this) {
                            fx = null
                            if (targetMb > 0) apply()
                        }
                    }
                }
            } catch (e: Throwable) {
                Log.w(TAG, "PREAMP|unavailable|${e.javaClass.simpleName}: ${e.message}")
                fx = null
                return describe()
            }
            fx = effect
        }
        try {
            effect.setTargetGain(targetMb)
            effect.setEnabled(targetMb > 0)
        } catch (e: Throwable) {
            Log.w(TAG, "PREAMP|failed|${e.javaClass.simpleName}: ${e.message}")
            return describe()
        }
        Log.i(TAG, "PREAMP|${describe()}")
        return describe()
    }

    private fun describe(): Map<String, Any?> {
        val effect = fx
        return mapOf(
            "session" to GLOBAL_SESSION,
            "requestedMb" to targetMb,
            "targetMb" to (effect?.let { runCatching { it.getTargetGain() }.getOrNull() } ?: 0),
            "enabled" to (effect?.let { runCatching { it.getEnabled() }.getOrNull() } ?: false),
            "control" to (effect?.let { runCatching { it.hasControl() }.getOrNull() } ?: false),
        )
    }
}

// Pure resolution logic for the stream (audio/subtitle) selection cascade
// (issue #3 / AGENTS.md "Client app structure"). Deliberately decoupled from
// fvp's VideoPlayerController/MediaInfo types (TrackCandidate below is the
// only input shape) so this can be unit-tested without a real player, and
// from StreamPreferencesService (this takes already-resolved cascades, not
// the cache) so it stays a small, side-effect-free function of its inputs.
import '../playhead_api.dart';

/// A candidate audio or subtitle track, reduced to just what the cascade
/// needs to match against. `index` is the position in the same list
/// fvp's `VideoPlayerController.getMediaInfo()` returned it from
/// (`info.audio[index]`/`info.subtitle[index]`) — the same indices
/// `setAudioTracks`/`setSubtitleTracks` expect (see
/// client/lib/widgets/debug_overlay.dart for how `info.audio` is read
/// elsewhere, and client/lib/main.dart's `_createController`, the call
/// site that applies the result of `resolve` below).
class TrackCandidate {
  final int index;
  // Lowercased ffmpeg-style "language" metadata tag, e.g. "por", "eng"; null
  // when the track carries no language metadata at all.
  final String? language;
  // Raw (not lowercased) "title" metadata tag, e.g. "Director's Commentary";
  // null when absent.
  final String? title;

  const TrackCandidate({required this.index, this.language, this.title});
}

/// The resolved selection to apply via
/// `controller.setAudioTracks`/`setSubtitleTracks`. `audioTracks == null`
/// means "no cascade step matched anything — leave the player's own default
/// alone" (never force a call); `subtitleTracks` is never null: an empty
/// list is an explicit "no subtitle", matching the behavior
/// `_createController` already had before this cascade existed
/// (`setSubtitleTracks([])`, unconditionally, to defeat fvp/libmdk's
/// default-enabled first MKV subtitle track).
class StreamSelection {
  final List<int>? audioTracks;
  final List<int> subtitleTracks;

  const StreamSelection({this.audioTracks, this.subtitleTracks = const []});

  static const none = StreamSelection(audioTracks: null, subtitleTracks: []);
}

/// Tier 1 (client-local item/channel override — see AGENTS.md "6-button
/// baseline" and widgets/track_selector_overlay.dart): the user's own
/// in-the-moment choice for the channel currently on screen, persisted by
/// SettingsService keyed by channel id (client-local only, per issue #3's
/// design — never sent to or stored by the backend, unlike tiers 2-4).
///
/// Unlike tiers 2-4 (which match by language/title metadata against a
/// cascade authored ahead of time), tier 1 is index-based: it's the literal
/// position the user picked in the SAME list `getMediaInfo()` returns (the
/// same contract TrackCandidate.index and setAudioTracks/setSubtitleTracks
/// already use) — simpler, and correct for its purpose: it's a choice made
/// by looking at THIS item's real track list, not a policy authored against
/// tracks that might not exist yet.
///
/// `audioIndex`/`subtitleIndex` are independent: an override on one axis
/// doesn't force the other to fall back to "none" — see `resolve` below for
/// why a tier 1 override is deliberately NOT atomic across both axes the way
/// a tiers 2-4 cascade step is.
class TierOneOverride {
  final int? audioIndex;
  final int? subtitleIndex; // -1 == the picker's explicit "no subtitle" choice

  const TierOneOverride({this.audioIndex, this.subtitleIndex});

  bool get isEmpty => audioIndex == null && subtitleIndex == null;
}

/// Resolves which audio/subtitle tracks to select. Tier 1 (client-local
/// override, see TierOneOverride) is consulted first, per axis; whichever
/// axis it didn't touch (or whose stored index doesn't exist in the
/// CURRENT item's track list — e.g. a different episode with fewer tracks)
/// falls through to tiers 2-4 in most-specific-first order (tier 2: user
/// profile, tier 3: channel admin, tier 4: global admin), first matching
/// cascade step wins. Each tier 2-4 cascade is tried whole before falling
/// through to the next tier — a tier with steps that all fail to match
/// doesn't get mixed with the next tier's steps.
class StreamSelectionResolver {
  const StreamSelectionResolver._();

  static StreamSelection resolve({
    required List<TrackCandidate> audioTracks,
    required List<TrackCandidate> subtitleTracks,
    TierOneOverride? tierOneOverride, // tier 1, tried first — see its doc comment
    required StreamRuleCascade userRules, // tier 2
    required StreamRuleCascade channelRules, // tier 3
    required StreamRuleCascade globalRules, // tier 4
  }) {
    List<int>? audioSelection;
    List<int>? subtitleSelection;

    if (tierOneOverride != null) {
      final audioIndex = tierOneOverride.audioIndex;
      if (audioIndex != null && audioIndex >= 0 && audioIndex < audioTracks.length) {
        audioSelection = [audioIndex];
      }
      final subtitleIndex = tierOneOverride.subtitleIndex;
      if (subtitleIndex == -1) {
        subtitleSelection = const [];
      } else if (subtitleIndex != null && subtitleIndex >= 0 && subtitleIndex < subtitleTracks.length) {
        subtitleSelection = [subtitleIndex];
      }
    }

    // Tiers 2-4 only fill in whichever axis tier 1 left untouched (or whose
    // stored index no longer exists in this item's track list) — a step
    // match is atomic across both axes (see _firstMatch), so the first tier
    // that matches at all supplies BOTH of whatever is still missing, same
    // as before tier 1 existed.
    if (audioSelection == null || subtitleSelection == null) {
      for (final cascade in [userRules, channelRules, globalRules]) {
        final match = _firstMatch(cascade, audioTracks, subtitleTracks);
        if (match != null) {
          audioSelection ??= match.audioTracks;
          subtitleSelection ??= match.subtitleTracks;
          break;
        }
      }
    }

    return StreamSelection(audioTracks: audioSelection, subtitleTracks: subtitleSelection ?? const []);
  }

  static StreamSelection? _firstMatch(
    StreamRuleCascade cascade,
    List<TrackCandidate> audioTracks,
    List<TrackCandidate> subtitleTracks,
  ) {
    for (final step in cascade.steps) {
      final audioMatches = audioTracks.where((t) => _audioMatchesStep(t, step)).toList();
      if (audioMatches.isEmpty) continue;

      // Atomic match: a step that demands a subtitle language but finds no
      // matching subtitle track fails as a whole (falls through to the next
      // step), rather than selecting the matched audio with no subtitle.
      List<int> subtitleSelection = const [];
      if (step.subtitleLanguage != null) {
        final subMatches = subtitleTracks.where((t) => _languageMatches(t.language, step.subtitleLanguage!)).toList();
        if (subMatches.isEmpty) continue;
        subtitleSelection = [subMatches.first.index];
      }

      return StreamSelection(audioTracks: [audioMatches.first.index], subtitleTracks: subtitleSelection);
    }
    return null;
  }

  static bool _audioMatchesStep(TrackCandidate track, StreamRuleStep step) {
    if (!_languageMatches(track.language, step.audioLanguage)) return false;
    final title = (track.title ?? '').toLowerCase();
    for (final blocked in step.titleBlocklist) {
      if (blocked.isNotEmpty && title.contains(blocked.toLowerCase())) return false;
    }
    if (step.titleAllowlist.isNotEmpty) {
      final allowed = step.titleAllowlist.any((a) => a.isNotEmpty && title.contains(a.toLowerCase()));
      if (!allowed) return false;
    }
    return true;
  }

  /// `pattern` is wildcard-capable: a trailing "*" matches any suffix (e.g.
  /// "pt*" matches "pt", "pt-br", "ptbr-something"); an empty pattern
  /// matches any (or absent) track language, on purpose — the deliberate way
  /// to express "any language is fine" at this step.
  static bool _languageMatches(String? trackLanguage, String pattern) {
    if (pattern.isEmpty) return true;
    final lang = (trackLanguage ?? '').toLowerCase();
    final pat = pattern.toLowerCase();
    if (pat.endsWith('*')) {
      return lang.startsWith(pat.substring(0, pat.length - 1));
    }
    return lang == pat;
  }
}

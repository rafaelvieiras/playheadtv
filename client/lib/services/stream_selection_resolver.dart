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

/// Resolves which audio/subtitle tracks to select, applying tiers 2-4 in
/// most-specific-first order (tier 2: user profile, tier 3: channel admin,
/// tier 4: global admin), first matching cascade step wins. Each tier's
/// cascade is tried whole before falling through to the next tier — a tier
/// with steps that all fail to match doesn't get mixed with the next tier's
/// steps.
class StreamSelectionResolver {
  const StreamSelectionResolver._();

  static StreamSelection resolve({
    required List<TrackCandidate> audioTracks,
    required List<TrackCandidate> subtitleTracks,
    required StreamRuleCascade userRules, // tier 2
    required StreamRuleCascade channelRules, // tier 3
    required StreamRuleCascade globalRules, // tier 4
  }) {
    // TODO(tier 1): an item/channel local override — an in-the-moment choice
    // the user makes on the client for what's currently showing — would be
    // consulted here, before tier 2, once it exists. It's explicitly out of
    // scope for this slice (client-local only, no UI designed yet — see the
    // tracking issue's "Open questions"); the cascade below effectively
    // starts at tier 2 until then.
    for (final cascade in [userRules, channelRules, globalRules]) {
      final match = _firstMatch(cascade, audioTracks, subtitleTracks);
      if (match != null) return match;
    }
    return StreamSelection.none;
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

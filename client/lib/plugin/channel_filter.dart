// Which channels this device can actually play.
//
// A channel's content can come from a plugin (docs/PLUGINS.md). Playing it needs
// that plugin installed here, so a channel whose plugin is missing is left out of
// the guide, the mosaic and the channel list: tuning into it could only produce an
// error screen nobody asked for.
//
// Two halves, on purpose. [PluginAvailability] answers the question once and
// holds the answer; [playableChannels] then filters with it synchronously. The
// filter has to be synchronous because it runs where the channel list enters the
// player and the mosaic (including from the cache-change callback, which is not
// async), and it must never put a directory read on that path.
//
// Requirements come from each channel's own cached guide, so nothing is fetched
// to answer it. A channel with no cached entry stays visible: hiding a channel on
// a cold start would look like data loss, and the first sync fills the cache in.
import '../cache/schedule_cache.dart';
import '../playhead_api.dart';
import 'plugin_manager.dart';
import 'plugin_manifest.dart';

/// A channel the viewer does not see, and why.
class HiddenChannel {
  final PlayheadChannel channel;

  /// One line per plugin that blocks it, ready to show.
  final List<String> reasons;

  const HiddenChannel(this.channel, this.reasons);

  String get reason => reasons.join(' ');
}

/// The channel list split in two.
class PlayableChannels {
  final List<PlayheadChannel> visible;
  final List<HiddenChannel> hidden;

  const PlayableChannels(this.visible, this.hidden);

  bool get isEmpty => visible.isEmpty;
  bool get isNotEmpty => visible.isNotEmpty;

  /// Nothing is left only because of missing plugins: a different thing to say
  /// than "the backend has no channels".
  bool get allHiddenByPlugins => visible.isEmpty && hidden.isNotEmpty;

  List<String> get reasons => [for (final channel in hidden) ...channel.reasons];
}

/// The plugins this device lacks, as of the last [refresh].
///
/// One shared instance lives in `OfflineRuntime`; the filter reads it, and
/// anything that changes what is installed (an install, a removal, a toggle, a
/// sync that revealed a new requirement) calls [refresh].
class PluginAvailability {
  final Set<String> _missingIds;
  final Map<String, MissingPlugin> _details;

  const PluginAvailability._(this._missingIds, this._details);

  /// Everything usable: the state before anything was checked, and the shape a
  /// test starts from.
  static const allUsable = PluginAvailability._({}, {});

  bool get isEmpty => _missingIds.isEmpty;

  /// Whether a channel needing [pluginIds] can be played here.
  bool canPlay(Iterable<String> pluginIds) {
    for (final id in pluginIds) {
      if (_missingIds.contains(id)) return false;
    }
    return true;
  }

  /// Why the first blocking plugin blocks, for a one-line notice.
  MissingPlugin? detailFor(Iterable<String> pluginIds) {
    for (final id in pluginIds) {
      final detail = _details[id];
      if (detail != null) return detail;
    }
    return null;
  }

  /// Recomputes the answer from what is installed and what the cached guide
  /// requires of every channel.
  ///
  /// One [PluginManager.missingFor] call covers every requirement at once, so
  /// this costs one directory read no matter how many channels there are, and a
  /// plugin id required by two channels is checked once.
  static Future<PluginAvailability> compute(
    ScheduleCache cache,
    List<PlayheadChannel> channels,
    PluginManager? plugins,
  ) async {
    final requirements = <String, EpgRequirement>{};
    for (final channel in channels) {
      for (final requirement in requirementsOf(cache, channel.id).values) {
        final existing = requirements[requirement.id];
        if (existing == null ||
            (requirement.minVersion != null &&
                (existing.minVersion == null ||
                    comparePluginVersions(requirement.minVersion!, existing.minVersion!) > 0))) {
          requirements[requirement.id] = requirement;
        }
      }
    }
    if (requirements.isEmpty) return allUsable;

    if (plugins == null) {
      // No manager yet: nothing that needs a plugin can be offered.
      return PluginAvailability._(
        {for (final id in requirements.keys) id},
        {for (final id in requirements.keys) id: MissingPlugin(id: id)},
      );
    }
    final missing = await plugins.missingFor(requirements.values);
    return PluginAvailability._(
      {for (final m in missing) m.id},
      {for (final m in missing) m.id: m},
    );
  }
}

/// Splits [channels] by whether this device can play them, using [availability].
PlayableChannels playableChannels(
  ScheduleCache cache,
  List<PlayheadChannel> channels,
  PluginAvailability availability,
) {
  if (channels.isEmpty || availability.isEmpty) return PlayableChannels(channels, const []);

  final visible = <PlayheadChannel>[];
  final hidden = <HiddenChannel>[];
  final seen = <String>{};
  for (final channel in channels) {
    final ids = requirementsOf(cache, channel.id).keys;
    if (availability.canPlay(ids)) {
      visible.add(channel);
      continue;
    }
    final reasons = <String>[];
    for (final id in ids) {
      final detail = availability.detailFor([id]);
      if (detail == null) continue;
      final line = detail.describe();
      if (seen.add(line)) reasons.add(line);
    }
    hidden.add(HiddenChannel(channel, reasons));
  }
  return PlayableChannels(visible, hidden);
}

/// The plugins a channel's cached guide needs, one entry per plugin id (the
/// strictest minimum wins).
Map<String, EpgRequirement> requirementsOf(ScheduleCache cache, String channelId) {
  final out = <String, EpgRequirement>{};
  for (final entry in cache.allEntries(channelId)) {
    for (final requirement in entry.requires) {
      final existing = out[requirement.id];
      if (existing == null) {
        out[requirement.id] = requirement;
        continue;
      }
      final oldMin = existing.minVersion;
      final newMin = requirement.minVersion;
      if (newMin != null && (oldMin == null || comparePluginVersions(newMin, oldMin) > 0)) {
        out[requirement.id] = requirement;
      }
    }
  }
  return out;
}

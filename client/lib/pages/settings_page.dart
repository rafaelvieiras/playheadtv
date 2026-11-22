// Settings screen: "Log level" (until restart), "Diagnostics" (see diagnostics_page.dart), "Remap controls" (see controls_mapping_page.dart), "Offline
// cache" (see offline_cache_page.dart) and, on desktop, "Start in full screen".
//
// Operable with the six baseline buttons: Up/Down move the focus between the
// rows, Select opens one (or flips the full screen row) and Back (Esc by
// default) returns to the mosaic.
import 'dart:async';
import 'dart:io' show Platform;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../input/input_action.dart';
import '../main.dart' show AppConfig;
import '../services/app_window.dart';
import '../clock.dart';
import '../dimmer.dart';
import '../epg_thumbnail.dart';
import '../preload_lead.dart';
import '../services/audio_preamp.dart';
import '../services/loudness_service.dart';
import '../services/settings_service.dart';
import '../ui/ui_scale.dart';
import '../widgets/focus_reveal.dart';
import '../video/video_engine.dart';
import '../log/log.dart';
import 'controls_mapping_page.dart';
import 'diagnostics_page.dart';
import 'offline_cache_page.dart';
import 'plugins_page.dart';
import 'server_url_page.dart';
import 'setup_page.dart';
import '../widgets/app_icons.dart';
import '../widgets/dimmer_level_dialog.dart';

class SettingsPage extends StatefulWidget {
  const SettingsPage({super.key});

  @override
  State<SettingsPage> createState() => _SettingsPageState();
}

class _SettingsPageState extends State<SettingsPage> {
  KeyBindingsService? _keyBindings;
  SettingsService? _settings;

  @override
  void initState() {
    super.initState();
    unawaited(SettingsService.create().then((settings) {
      if (!mounted) return;
      setState(() {
        _settings = settings;
        _keyBindings = KeyBindingsService(settings);
      });
    }));
  }

  // Saves the choice and applies it to the window right away.
  void _toggleFullScreen() {
    final settings = _settings;
    if (settings == null) return;
    final next = !settings.startFullScreen(AppConfig.startFullScreenDefault);
    unawaited(settings.setStartFullScreen(next).then((_) {
      if (mounted) setState(() {});
    }));
    unawaited(FullScreenController.shared.apply(next));
  }

  // Cycles Automatic, 75%, 100%, ... and applies the choice at once.
  void _cycleUiScale() {
    final settings = _settings;
    if (settings == null) return;
    final next = nextUiScaleChoice(settings.uiScaleOverride);
    unawaited(settings.setUiScaleOverride(next).then((_) {
      if (mounted) setState(() {});
    }));
    UiScaleOverride.notifier.value = next;
  }

  // Leveling: the on/off switch and the two numbers it is made of. The pre-amp
  // is the only gain above unity the app has, so it is the knob that answers
  // "everything is too quiet"; the target is what the channels are brought to.
  void _toggleLoudness() {
    final settings = _settings;
    if (settings == null) return;
    final next = !(settings.loudnessEnabled ?? AppConfig.loudnessEnabled);
    unawaited(settings.setLoudnessEnabled(next).then((_) {
      if (mounted) setState(() {});
    }));
    // The remembered gains were computed under the old setting.
    unawaited(LoudnessService.create().then((s) => s.clearItems()));
    if (!next) unawaited(const AudioPreamp().set(0));
  }

  void _cycleLoudnessTarget() {
    final settings = _settings;
    if (settings == null) return;
    final next = nextLoudnessTargetChoice(settings.loudnessTargetLufs ?? AppConfig.loudnessTargetLufs);
    unawaited(settings.setLoudnessTargetLufs(next).then((_) {
      if (mounted) setState(() {});
    }));
    unawaited(LoudnessService.create().then((s) => s.clearItems()));
  }

  void _cycleLoudnessPreamp() {
    final settings = _settings;
    if (settings == null) return;
    final next = nextLoudnessPreampChoice(settings.loudnessPreampDb ?? AppConfig.loudnessPreampDb);
    unawaited(settings.setLoudnessPreampDb(next).then((_) {
      if (mounted) setState(() {});
    }));
    // Unlike the target, this one can be heard immediately.
    unawaited(const AudioPreamp().set(next));
    unawaited(LoudnessService.create().then((s) => s.clearItems()));
  }

  // Select steps through the preload lead choices (seconds) and saves.
  void _cyclePreloadLead() {
    final settings = _settings;
    if (settings == null) return;
    final current = settings.preloadLeadSeconds ?? AppConfig.preloadLeadDefaultSeconds;
    unawaited(settings.setPreloadLeadSeconds(nextPreloadLeadChoice(current)).then((_) {
      if (mounted) setState(() {});
    }));
  }

  // Which engine is saved, as it will apply at the next start. Settings only
  // offers the choice where there is one (Android).
  VideoEngineKind get _savedVideoEngine => resolveVideoEngineKind(_settings?.videoEngine, isAndroid: Platform.isAndroid);

  void _toggleVideoEngine() {
    final settings = _settings;
    if (settings == null) return;
    final next = _savedVideoEngine == VideoEngineKind.exo ? VideoEngineKind.fvp : VideoEngineKind.exo;
    unawaited(settings.setVideoEngine(next.name).then((_) {
      if (mounted) setState(() {});
    }));
  }

  // Select steps through the log levels. Applies at once and only until the app
  // restarts (never saved): a forgotten verbose client must not keep logging.
  void _cycleLogLevel() {
    Log.setRuntimeLevel(nextLogLevelChoice(Log.level));
    setState(() {});
  }

  // Select flips whether the guide shows the channel thumbnail, and applies it at once.
  void _toggleEpgThumbnail() {
    final settings = _settings;
    if (settings == null) return;
    final next = !settings.epgThumbnail;
    EpgThumbnailSetting.enabled.value = next;
    unawaited(settings.setEpgThumbnail(next).then((_) {
      if (mounted) setState(() {});
    }));
  }

  // Select flips the always-on clock and applies it at once.
  void _toggleClock() {
    final settings = _settings;
    if (settings == null) return;
    final next = !settings.clockAlwaysVisible;
    ClockOverlaySettings.alwaysVisible.value = next;
    unawaited(settings.setClockAlwaysVisible(next).then((_) {
      if (mounted) setState(() {});
    }));
  }

  // Select steps through the clock's opacity choices and saves.
  void _cycleClockOpacity() {
    final settings = _settings;
    if (settings == null) return;
    final next = nextClockOpacityChoice(settings.clockOpacityPct);
    ClockOverlaySettings.opacityPct.value = next;
    unawaited(settings.setClockOpacityPct(next).then((_) {
      if (mounted) setState(() {});
    }));
  }

  // Select steps through the clock's size choices and saves.
  void _cycleClockScale() {
    final settings = _settings;
    if (settings == null) return;
    final next = nextClockScaleChoice(settings.clockScale);
    ClockOverlaySettings.scale.value = next;
    unawaited(settings.setClockScale(next).then((_) {
      if (mounted) setState(() {});
    }));
  }

  // Opens the dim level dialog (six buttons) and saves the confirmed level.
  Future<void> _editDimLevel() async {
    final settings = _settings;
    final keyBindings = _keyBindings;
    if (settings == null || keyBindings == null) return;
    final chosen = await showDimmerLevelDialog(
      context,
      initial: AppConfig.configuredDimLevel(settings),
      resolve: keyBindings.resolve,
    );
    if (chosen == null) return;
    await settings.setDimLevelPercent(chosen);
    Dimmer.level.value = chosen;
    if (mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    return UiScaled(child: _buildPage(context));
  }

  Widget _buildPage(BuildContext context) {
    return Focus(
      // Only catches Back bubbling up from a row: it must never be a traversal
      // target itself (it would take focus past the last row).
      canRequestFocus: false,
      skipTraversal: true,
      onKeyEvent: (node, event) {
        if (event is KeyDownEvent && _keyBindings?.resolve(event.logicalKey) == InputAction.back) {
          Navigator.of(context).maybePop();
          return KeyEventResult.handled;
        }
        return KeyEventResult.ignored;
      },
      child: Scaffold(
        key: const Key('settings-page'),
        appBar: AppBar(title: const Text('Settings'), toolbarHeight: context.ui(kToolbarHeight)),
        body: FocusReveal(
          child: SingleChildScrollView(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                ListTile(
                  key: const Key('settings-connection-tile'),
                  leading: const Icon(Icons.lan),
                  title: const Text('Connection'),
                  subtitle: const Text('Media server and Playhead TV addresses'),
                  onTap: () {
                    Navigator.of(context).push(MaterialPageRoute(builder: (_) => const SetupPage()));
                  },
                ),
                ListTile(
                  key: const Key('settings-server-tile'),
                  leading: const Icon(Icons.dns),
                  title: const Text('Server'),
                  subtitle: const Text('Change server or pair this device again'),
                  onTap: () {
                    Navigator.of(context).push(MaterialPageRoute(builder: (_) => const ServerUrlPage()));
                  },
                ),
                ListTile(
                  key: const Key('settings-controls-tile'),
                  autofocus: true,
                  leading: const Icon(AppIcons.controls),
                  title: const Text('Remap controls'),
                  onTap: () {
                    Navigator.of(context).push(MaterialPageRoute(builder: (_) => const ControlsMappingPage()));
                  },
                ),
                ListTile(
                  key: const Key('settings-offline-tile'),
                  leading: const Icon(AppIcons.offlineCache),
                  title: const Text('Offline cache'),
                  onTap: () {
                    Navigator.of(context).push(MaterialPageRoute(builder: (_) => const OfflineCachePage()));
                  },
                ),
                ListTile(
                  key: const Key('settings-plugins-tile'),
                  leading: const Icon(AppIcons.plugin),
                  title: const Text('Plugins'),
                  subtitle: const Text('Content sources outside the media server'),
                  onTap: () {
                    Navigator.of(context).push(MaterialPageRoute(builder: (_) => const PluginsPage()));
                  },
                ),
                // The window is not the app's to resize on Android (always full
                // screen): no row there.
                if (_settings != null && FullScreenController.shared.supported)
                  ListTile(
                    key: const Key('settings-fullscreen-tile'),
                    leading: const Icon(AppIcons.fullScreen),
                    title: const Text('Start in full screen'),
                    subtitle: Text(
                      _settings!.startFullScreen(AppConfig.startFullScreenDefault) ? 'On' : 'Off',
                      key: const Key('settings-fullscreen-value'),
                    ),
                    onTap: _toggleFullScreen,
                  ),
                if (_settings != null)
                  ListTile(
                    key: const Key('settings-ui-scale-tile'),
                    leading: const Icon(AppIcons.interfaceSize),
                    title: const Text('Interface size'),
                    subtitle: Text(
                      uiScaleLabel(_settings!.uiScaleOverride),
                      key: const Key('settings-ui-scale-value'),
                    ),
                    onTap: _cycleUiScale,
                  ),
                if (_settings != null)
                  ListTile(
                    key: const Key('settings-preload-lead-tile'),
                    leading: const Icon(AppIcons.preloadLead),
                    title: const Text('Preload lead'),
                    subtitle: Text(
                      preloadLeadLabel(_settings!.preloadLeadSeconds ?? AppConfig.preloadLeadDefaultSeconds),
                      key: const Key('settings-preload-lead-value'),
                    ),
                    onTap: _cyclePreloadLead,
                  ),
                if (_settings != null)
                  ListTile(
                    key: const Key('settings-dim-level-tile'),
                    leading: const Icon(AppIcons.dimmer),
                    title: const Text('Dim level'),
                    subtitle: Text(
                      dimLevelLabel(AppConfig.configuredDimLevel(_settings!)),
                      key: const Key('settings-dim-level-value'),
                    ),
                    onTap: _editDimLevel,
                  ),
                if (_settings != null)
                  ListTile(
                    key: const Key('settings-clock-tile'),
                    leading: const Icon(AppIcons.clock),
                    title: const Text('Always show the clock'),
                    subtitle: Text(
                      _settings!.clockAlwaysVisible ? 'On' : 'Off',
                      key: const Key('settings-clock-value'),
                    ),
                    onTap: _toggleClock,
                  ),
                // The two looks only matter while the clock is on: no rows when it
                // is off, so Settings does not offer knobs for something the player
                // never draws.
                if (_settings != null && _settings!.clockAlwaysVisible)
                  ListTile(
                    key: const Key('settings-clock-opacity-tile'),
                    leading: const Icon(AppIcons.clock),
                    title: const Text('Clock opacity'),
                    subtitle: Text(
                      clockOpacityLabel(_settings!.clockOpacityPct),
                      key: const Key('settings-clock-opacity-value'),
                    ),
                    onTap: _cycleClockOpacity,
                  ),
                if (_settings != null && _settings!.clockAlwaysVisible)
                  ListTile(
                    key: const Key('settings-clock-scale-tile'),
                    leading: const Icon(AppIcons.clock),
                    title: const Text('Clock size'),
                    subtitle: Text(
                      clockScaleLabel(_settings!.clockScale),
                      key: const Key('settings-clock-scale-value'),
                    ),
                    onTap: _cycleClockScale,
                  ),
                if (_settings != null)
                  ListTile(
                    key: const Key('settings-epg-thumbnail-tile'),
                    leading: const Icon(AppIcons.epgThumbnail),
                    title: const Text('Show channel thumbnail in the guide'),
                    subtitle: Text(
                      _settings!.epgThumbnail ? 'On' : 'Off',
                      key: const Key('settings-epg-thumbnail-value'),
                    ),
                    onTap: _toggleEpgThumbnail,
                  ),
                if (_settings != null)
                  ListTile(
                    key: const Key('settings-loudness-tile'),
                    leading: const Icon(AppIcons.volume),
                    title: const Text('Level the channels'),
                    subtitle: Text(
                      (_settings!.loudnessEnabled ?? AppConfig.loudnessEnabled) ? 'On' : 'Off',
                      key: const Key('settings-loudness-value'),
                    ),
                    onTap: _toggleLoudness,
                  ),
                if (_settings != null && (_settings!.loudnessEnabled ?? AppConfig.loudnessEnabled))
                  ListTile(
                    key: const Key('settings-loudness-target-tile'),
                    leading: const Icon(AppIcons.volume),
                    title: const Text('Leveling target'),
                    subtitle: Text(
                      loudnessTargetLabel(_settings!.loudnessTargetLufs ?? AppConfig.loudnessTargetLufs),
                      key: const Key('settings-loudness-target-value'),
                    ),
                    onTap: _cycleLoudnessTarget,
                  ),
                if (_settings != null && (_settings!.loudnessEnabled ?? AppConfig.loudnessEnabled))
                  ListTile(
                    key: const Key('settings-loudness-preamp-tile'),
                    leading: const Icon(AppIcons.volume),
                    title: const Text('Pre-amp (boost)'),
                    subtitle: Text(
                      loudnessPreampLabel(_settings!.loudnessPreampDb ?? AppConfig.loudnessPreampDb),
                      key: const Key('settings-loudness-preamp-value'),
                    ),
                    onTap: _cycleLoudnessPreamp,
                  ),
                if (_settings != null && Platform.isAndroid)
                  ListTile(
                    key: const Key('settings-video-engine-tile'),
                    leading: const Icon(AppIcons.videoEngine),
                    title: const Text('Video player'),
                    subtitle: Text(
                      videoEngineLabel(_savedVideoEngine, running: videoEngine),
                      key: const Key('settings-video-engine-value'),
                    ),
                    onTap: _toggleVideoEngine,
                  ),
                ListTile(
                  key: const Key('settings-log-level-tile'),
                  leading: const Icon(AppIcons.logLevel),
                  title: const Text('Log level'),
                  subtitle: Text(logLevelLabel(Log.level, Log.configuredLevel), key: const Key('settings-log-level-value')),
                  onTap: _cycleLogLevel,
                ),
                ListTile(
                  key: const Key('settings-diagnostics-tile'),
                  leading: const Icon(AppIcons.diagnostics),
                  title: const Text('Diagnostics'),
                  subtitle: const Text('Log file location and a shareable log bundle'),
                  onTap: () {
                    Navigator.of(context).push(MaterialPageRoute(builder: (_) => const DiagnosticsPage()));
                  },
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

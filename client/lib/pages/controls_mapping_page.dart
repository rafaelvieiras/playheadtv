// "Remap controls" screen: lists every InputAction grouped by category, shows
// each one's current key (default or override) via
// KeyBindingsService.effectiveBindings, and lets you remap by tapping "Change"
// + pressing the new key (capture mode). "Restore defaults" clears all
// overrides at once.
//
// No binding-conflict detection and no reserved-key list — out of scope for
// now.
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../input/input_action.dart';
import '../services/settings_service.dart';

class _ActionGroup {
  final String title;
  final List<InputAction> actions;
  const _ActionGroup(this.title, this.actions);
}

const _groups = [
  _ActionGroup('Navigation', [
    InputAction.channelPrevious,
    InputAction.channelNext,
    InputAction.digit0,
    InputAction.digit1,
    InputAction.digit2,
    InputAction.digit3,
    InputAction.digit4,
    InputAction.digit5,
    InputAction.digit6,
    InputAction.digit7,
    InputAction.digit8,
    InputAction.digit9,
  ]),
  _ActionGroup('Playback', [
    InputAction.select,
    InputAction.back,
    InputAction.trackSelector,
  ]),
  _ActionGroup('General', [
    InputAction.home,
    InputAction.debugOverlay,
  ]),
  // Path 2 accelerators (see AGENTS.md's "6-button baseline" / accelerator
  // exception, and input/input_action.dart): neither has a default binding —
  // they only exist here, unbound, until the user picks one. The
  // 6-button-only equivalent (the track selector overlay, InputAction.
  // trackSelector above) always works with zero remapping.
  _ActionGroup('Shortcuts (optional)', [
    InputAction.cycleAudioTrack,
    InputAction.cycleSubtitleTrack,
  ]),
];

String _actionLabel(InputAction action) {
  final digit = action.digitValue;
  if (digit != null) return 'Digit $digit';
  switch (action) {
    case InputAction.channelNext:
      return 'Next channel';
    case InputAction.channelPrevious:
      return 'Previous channel';
    case InputAction.select:
      return 'Select / Info / EPG';
    case InputAction.back:
      return 'Back';
    case InputAction.home:
      return 'Home';
    case InputAction.debugOverlay:
      return 'Debug overlay (FPS/CPU/video)';
    case InputAction.trackSelector:
      return 'Track selector (audio/subtitle)';
    case InputAction.cycleAudioTrack:
      return 'Cycle audio track (accelerator)';
    case InputAction.cycleSubtitleTrack:
      return 'Cycle subtitle track (accelerator)';
    default:
      return action.name;
  }
}

String _keyLabel(LogicalKeyboardKey key) => key.debugName ?? key.keyLabel;

class ControlsMappingPage extends StatefulWidget {
  const ControlsMappingPage({super.key});

  @override
  State<ControlsMappingPage> createState() => _ControlsMappingPageState();
}

class _ControlsMappingPageState extends State<ControlsMappingPage> {
  SettingsService? _settings;
  KeyBindingsService? _keyBindings;
  Map<InputAction, LogicalKeyboardKey> _bindings = {};
  InputAction? _capturingAction;

  final FocusNode _pageFocusNode = FocusNode();

  @override
  void initState() {
    super.initState();
    _bootstrap();
  }

  Future<void> _bootstrap() async {
    final settings = await SettingsService.create();
    if (!mounted) return;
    setState(() {
      _settings = settings;
      _keyBindings = KeyBindingsService(settings);
      _bindings = _keyBindings!.effectiveBindings;
    });
  }

  void _startCapture(InputAction action) {
    setState(() => _capturingAction = action);
  }

  Future<void> _handleKeyEvent(KeyEvent event) async {
    final capturing = _capturingAction;
    if (event is! KeyDownEvent) return;
    if (capturing == null) {
      // Back leaves the screen (while capturing, every key is the new binding).
      if (_keyBindings?.resolve(event.logicalKey) == InputAction.back && mounted) {
        unawaited(Navigator.of(context).maybePop());
      }
      return;
    }
    await _keyBindings!.rebind(capturing, event.logicalKey);
    if (!mounted) return;
    setState(() {
      _bindings = _keyBindings!.effectiveBindings;
      _capturingAction = null;
    });
  }

  Future<void> _restoreDefaults() async {
    await _keyBindings!.restoreDefaults();
    if (!mounted) return;
    setState(() => _bindings = _keyBindings!.effectiveBindings);
  }

  @override
  void dispose() {
    _pageFocusNode.dispose();
    super.dispose();
  }

  Widget _buildActionTile(InputAction action) {
    final capturing = _capturingAction == action;
    final boundKey = _bindings[action];
    return ListTile(
      title: Text(_actionLabel(action)),
      subtitle: capturing
          ? const Text('Press a key...', key: Key('capturing-label'))
          : Text(
              // An accelerator (cycleAudioTrack/cycleSubtitleTrack) has no
              // default — until the user assigns one via "Change" below, it
              // simply isn't in `_bindings` (see KeyBindingsService.
              // effectiveBindings' doc comment).
              boundKey != null ? _keyLabel(boundKey) : 'Not set',
              key: Key('binding-label-${action.name}'),
            ),
      trailing: ElevatedButton(
        key: Key('remap-button-${action.name}'),
        onPressed: () => _startCapture(action),
        child: const Text('Change'),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    if (_settings == null || _keyBindings == null) {
      return const Scaffold(
        key: Key('controls-mapping-page'),
        body: Center(child: CircularProgressIndicator()),
      );
    }

    return KeyboardListener(
      focusNode: _pageFocusNode,
      autofocus: true,
      onKeyEvent: (event) {
        unawaited(_handleKeyEvent(event));
      },
      child: Scaffold(
        key: const Key('controls-mapping-page'),
        appBar: AppBar(
          title: const Text('Remap controls'),
          actions: [
            TextButton(
              key: const Key('restore-defaults-button'),
              onPressed: _restoreDefaults,
              child: const Text('Restore defaults', style: TextStyle(color: Colors.white)),
            ),
          ],
        ),
        body: ListView(
          children: [
            for (final group in _groups) ...[
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 16, 16, 4),
                child: Text(
                  group.title,
                  style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 16),
                ),
              ),
              for (final action in group.actions) _buildActionTile(action),
            ],
          ],
        ),
      ),
    );
  }
}

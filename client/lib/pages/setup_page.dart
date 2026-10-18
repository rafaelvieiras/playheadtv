// Setup screen: where the media server and the Playhead TV backend are. It is
// the first thing a fresh install shows (no config file, no build-time defines,
// nothing typed yet) and is also reachable from Settings -> "Connection".
//
// Operable with the six baseline buttons: Up/Down move between the fields and
// buttons, Select on a field opens the platform's text entry (the on-screen
// keyboard on a TV) and Select on a button acts. Back leaves a Settings visit;
// on the first run there is nowhere to go back to, so the system closes the app.
//
// "Test connection" asks each server one cheap question and says in a sentence
// what is wrong. Save is allowed whenever the typed values are valid: a server
// that is down right now (an offline-first client) must not block saving.
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:http/http.dart' as http;

import '../config/connection_probe.dart';
import '../config/connection_settings.dart';
import '../input/input_action.dart';
import '../log/log.dart';
import '../main.dart' show AppConfig;
import '../services/settings_service.dart';
import '../ui/ui_scale.dart';

/// Called after a successful save with the setup page's own context. The first
/// run uses it to move on to the home page; a Settings visit leaves it null and
/// just pops.
typedef SetupSaved = void Function(BuildContext context);

class SetupPage extends StatefulWidget {
  /// First run: no Back to Settings, the title welcomes instead of "Connection".
  final bool firstRun;
  final SetupSaved? onSaved;

  /// Replaced by tests (probes use it).
  final http.Client? httpClient;

  const SetupPage({super.key, this.firstRun = false, this.onSaved, this.httpClient});

  @override
  State<SetupPage> createState() => _SetupPageState();
}

class _SetupPageState extends State<SetupPage> {
  final _jellyfinUrl = TextEditingController();
  final _jellyfinKey = TextEditingController();
  final _backendUrl = TextEditingController();
  final _deviceToken = TextEditingController();

  SettingsService? _settings;
  KeyBindingsService? _keyBindings;
  bool _testing = false;
  ProbeResult? _mediaResult;
  ProbeResult? _backendResult;
  String? _error;

  @override
  void initState() {
    super.initState();
    unawaited(SettingsService.create().then((settings) {
      if (!mounted) return;
      final stored = settings.connection;
      // Show what is in effect (a config file or a build can set values too),
      // falling back to what was typed before.
      _jellyfinUrl.text = AppConfig.jellyfinUrl.isNotEmpty ? AppConfig.jellyfinUrl : stored.jellyfinUrl;
      _jellyfinKey.text = AppConfig.jellyfinApiKey.isNotEmpty ? AppConfig.jellyfinApiKey : stored.jellyfinApiKey;
      _backendUrl.text = AppConfig.playheadBaseUrl.isNotEmpty ? AppConfig.playheadBaseUrl : stored.playheadBaseUrl;
      _deviceToken.text =
          AppConfig.playheadDeviceToken.isNotEmpty ? AppConfig.playheadDeviceToken : stored.playheadDeviceToken;
      setState(() {
        _settings = settings;
        _keyBindings = KeyBindingsService(settings);
      });
    }));
  }

  @override
  void dispose() {
    _jellyfinUrl.dispose();
    _jellyfinKey.dispose();
    _backendUrl.dispose();
    _deviceToken.dispose();
    super.dispose();
  }

  ConnectionSettings get _typed => ConnectionSettings(
        jellyfinUrl: _jellyfinUrl.text,
        jellyfinApiKey: _jellyfinKey.text,
        playheadBaseUrl: _backendUrl.text,
        playheadDeviceToken: _deviceToken.text,
      ).normalized();

  Future<void> _test() async {
    final value = _typed;
    final problem = value.validate();
    if (problem != null) {
      setState(() => _error = problem);
      return;
    }
    setState(() {
      _error = null;
      _testing = true;
      _mediaResult = null;
      _backendResult = null;
    });
    final results = await Future.wait([
      probeMediaServer(value.jellyfinUrl, value.jellyfinApiKey, client: widget.httpClient),
      probeBackend(value.playheadBaseUrl, value.playheadDeviceToken, client: widget.httpClient),
    ]);
    if (!mounted) return;
    Log.info('setup', 'connection test: media=${results[0].status.name} backend=${results[1].status.name}');
    setState(() {
      _testing = false;
      _mediaResult = results[0];
      _backendResult = results[1];
    });
  }

  Future<void> _save() async {
    final settings = _settings;
    if (settings == null) return;
    final value = _typed;
    final problem = value.validate();
    if (problem != null) {
      setState(() => _error = problem);
      return;
    }
    await settings.setConnection(value);
    AppConfig.applyStoredConnection(value);
    Log.info('setup', 'connection saved');
    if (!mounted) return;
    final onSaved = widget.onSaved;
    if (onSaved != null) {
      onSaved(context);
    } else {
      Navigator.of(context).maybePop();
    }
  }

  @override
  Widget build(BuildContext context) => UiScaled(child: _buildPage(context));

  Widget _field(String key, TextEditingController controller, String label,
      {String? hint, bool autofocus = false, bool last = false}) {
    return Padding(
      padding: EdgeInsets.symmetric(horizontal: context.ui(24), vertical: context.ui(6)),
      child: TextField(
        key: Key(key),
        controller: controller,
        autofocus: autofocus,
        autocorrect: false,
        enableSuggestions: false,
        keyboardType: TextInputType.url,
        textInputAction: last ? TextInputAction.done : TextInputAction.next,
        style: TextStyle(fontSize: context.ui(22)),
        decoration: InputDecoration(
          labelText: label,
          hintText: hint,
          border: const OutlineInputBorder(),
          labelStyle: TextStyle(fontSize: context.ui(20)),
        ),
        onChanged: (_) {
          if (_error != null) setState(() => _error = null);
        },
        onSubmitted: (_) => last ? unawaited(_save()) : FocusScope.of(context).nextFocus(),
      ),
    );
  }

  Widget _result(String key, String label, ProbeResult? result) {
    if (result == null) return const SizedBox.shrink();
    final color = result.isOk ? Colors.greenAccent : Colors.redAccent;
    return Padding(
      padding: EdgeInsets.symmetric(horizontal: context.ui(24), vertical: context.ui(2)),
      child: Row(children: [
        Icon(result.isOk ? Icons.check_circle : Icons.error, color: color, size: context.ui(24)),
        SizedBox(width: context.ui(10)),
        Expanded(
          child: Text('$label: ${result.message}',
              key: Key(key), style: TextStyle(fontSize: context.ui(20), color: color)),
        ),
      ]),
    );
  }

  Widget _buildPage(BuildContext context) {
    final ready = _settings != null;
    return Focus(
      // Only catches Back bubbling up from a field or button: never a traversal
      // target itself (it would take focus past the last row).
      canRequestFocus: false,
      skipTraversal: true,
      onKeyEvent: (node, event) {
        if (!widget.firstRun &&
            event is KeyDownEvent &&
            _keyBindings?.resolve(event.logicalKey) == InputAction.back) {
          Navigator.of(context).maybePop();
          return KeyEventResult.handled;
        }
        return KeyEventResult.ignored;
      },
      child: Scaffold(
        key: const Key('setup-page'),
        appBar: AppBar(
          title: Text(widget.firstRun ? 'Welcome to Playhead TV' : 'Connection'),
          toolbarHeight: context.ui(kToolbarHeight),
          automaticallyImplyLeading: !widget.firstRun,
        ),
        body: !ready
            ? const Center(child: CircularProgressIndicator())
            : FocusTraversalGroup(
                policy: OrderedTraversalPolicy(),
                child: ListView(
                  padding: EdgeInsets.symmetric(vertical: context.ui(12)),
                  children: [
                    Padding(
                      padding: EdgeInsets.symmetric(horizontal: context.ui(24), vertical: context.ui(8)),
                      child: Text(
                        'Enter the address of your media server and of your Playhead TV backend. '
                        'Use the address of the machine on your network, not "localhost".',
                        style: TextStyle(fontSize: context.ui(20)),
                      ),
                    ),
                    _field('setup-jellyfin-url', _jellyfinUrl, 'Media server URL',
                        hint: 'http://192.168.1.10:8096', autofocus: true),
                    _field('setup-jellyfin-key', _jellyfinKey, 'Media server API key'),
                    _field('setup-backend-url', _backendUrl, 'Playhead TV URL', hint: 'http://192.168.1.10:9394'),
                    _field('setup-device-token', _deviceToken, 'Device token (optional)', last: true),
                    if (_error != null)
                      Padding(
                        padding: EdgeInsets.symmetric(horizontal: context.ui(24), vertical: context.ui(6)),
                        child: Text(_error!,
                            key: const Key('setup-error'),
                            style: TextStyle(fontSize: context.ui(20), color: Colors.redAccent)),
                      ),
                    if (_testing)
                      Padding(
                        padding: EdgeInsets.all(context.ui(12)),
                        child: const Center(child: CircularProgressIndicator()),
                      ),
                    _result('setup-media-result', 'Media server', _mediaResult),
                    _result('setup-backend-result', 'Backend', _backendResult),
                    SizedBox(height: context.ui(12)),
                    Padding(
                      padding: EdgeInsets.symmetric(horizontal: context.ui(24)),
                      child: Row(children: [
                        OutlinedButton(
                          key: const Key('setup-test-button'),
                          onPressed: _testing ? null : () => unawaited(_test()),
                          child: Padding(
                            padding: EdgeInsets.all(context.ui(10)),
                            child: Text('Test connection', style: TextStyle(fontSize: context.ui(20))),
                          ),
                        ),
                        SizedBox(width: context.ui(16)),
                        FilledButton(
                          key: const Key('setup-save-button'),
                          onPressed: () => unawaited(_save()),
                          child: Padding(
                            padding: EdgeInsets.all(context.ui(10)),
                            child: Text('Save', style: TextStyle(fontSize: context.ui(20))),
                          ),
                        ),
                      ]),
                    ),
                  ],
                ),
              ),
      ),
    );
  }
}

// First-run server address screen: the user types the Playhead TV backend
// address, the page normalises it and probes GET /version, then hands off to
// the quick-connect pairing page. Reached on first run when no server URL is
// known, and from PairingPage's "Select another server" button.
//
// Operable with the six baseline buttons: the address field swallows Up/Down
// (caret movement) and Select on a TV remote does not open the on-screen
// keyboard by itself, so its own focus node translates the six buttons — the
// same pattern as SetupPage. Back leaves the page when there is somewhere to
// return to.
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:http/http.dart' as http;

import '../config/connection_probe.dart';
import '../input/input_action.dart';
import '../log/log.dart';
import '../services/pairing_boot.dart' show ServerUrlResolver;
import '../services/settings_service.dart';
import '../ui/ui_scale.dart';
import 'pairing_page.dart';

class ServerUrlPage extends StatefulWidget {
  /// Replaced by tests (the version probe uses it).
  final http.Client? httpClient;

  const ServerUrlPage({super.key, this.httpClient});

  @override
  State<ServerUrlPage> createState() => _ServerUrlPageState();
}

class _ServerUrlPageState extends State<ServerUrlPage> {
  final _url = TextEditingController();
  late final FocusNode _node = FocusNode(debugLabel: 'server-url-field', onKeyEvent: _fieldKey);

  SettingsService? _settings;
  KeyBindingsService? _keyBindings;
  bool _probing = false;
  String? _error;

  @override
  void initState() {
    super.initState();
    unawaited(SettingsService.create().then((settings) {
      if (!mounted) return;
      _url.text = settings.chosenServerUrl;
      setState(() {
        _settings = settings;
        _keyBindings = KeyBindingsService(settings);
      });
    }));
  }

  // Up/Down move between the field and the buttons; Select/Enter opens the
  // platform's text entry. Everything else (letters) goes to the field.
  KeyEventResult _fieldKey(FocusNode node, KeyEvent event) {
    if (event is KeyUpEvent) return KeyEventResult.ignored;
    final key = event.logicalKey;
    if (key == LogicalKeyboardKey.arrowDown) {
      node.nextFocus();
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.arrowUp) {
      node.previousFocus();
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.select || key == LogicalKeyboardKey.enter || key == LogicalKeyboardKey.numpadEnter) {
      if (event is KeyDownEvent) unawaited(SystemChannels.textInput.invokeMethod<void>('TextInput.show'));
      return KeyEventResult.handled;
    }
    return KeyEventResult.ignored;
  }

  @override
  void dispose() {
    _node.dispose();
    _url.dispose();
    super.dispose();
  }

  Future<void> _continue() async {
    final settings = _settings;
    if (settings == null) return;
    final url = ServerUrlResolver.normalize(_url.text);
    if (url.isEmpty) {
      setState(() => _error = 'Enter a server address like http://server.example.com:9394.');
      return;
    }
    setState(() {
      _error = null;
      _probing = true;
    });
    final result = await _probeVersion(url, client: widget.httpClient);
    if (!mounted) return;
    if (!result.isOk) {
      setState(() {
        _probing = false;
        _error = result.message;
      });
      return;
    }
    await settings.setChosenServerUrl(url);
    Log.info('pairing', 'server address accepted');
    if (!mounted) return;
    Navigator.of(context).pushReplacement(MaterialPageRoute(
      builder: (_) => PairingPage(serverUrl: url, httpClient: widget.httpClient),
    ));
  }

  /// GET /version: the cheapest question that tells a Playhead TV backend from
  /// anything else on the address. Mirrors the probe style in
  /// config/connection_probe.dart; never throws and echoes no key or token.
  Future<ProbeResult> _probeVersion(
    String baseUrl, {
    http.Client? client,
    Duration timeout = const Duration(seconds: 6),
  }) async {
    final owned = client == null;
    final c = client ?? http.Client();
    try {
      final response = await c.get(Uri.parse('$baseUrl/version')).timeout(timeout);
      final code = response.statusCode;
      if (code == 200) return const ProbeResult(ProbeStatus.ok, 'Playhead TV server reachable.');
      if (code == 404) {
        return const ProbeResult(
            ProbeStatus.unexpected, 'Reached a server, but it does not look like a Playhead TV server (404).');
      }
      return ProbeResult(ProbeStatus.unexpected, 'The server answered with HTTP $code.');
    } on TimeoutException {
      return const ProbeResult(
          ProbeStatus.unreachable, 'No answer: check the address and that the device is on the same network.');
    } on Object {
      return const ProbeResult(ProbeStatus.unreachable, 'Could not connect: check the address and the network.');
    } finally {
      if (owned) c.close();
    }
  }

  @override
  Widget build(BuildContext context) => UiScaled(child: _buildPage(context));

  Widget _buildPage(BuildContext context) {
    final ready = _settings != null;
    return Focus(
      // Only catches Back bubbling up from the field or buttons: never a
      // traversal target itself.
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
        key: const Key('server-url-page'),
        appBar: AppBar(
          title: const Text('Server address'),
          toolbarHeight: context.ui(kToolbarHeight),
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
                        'Enter the address of your Playhead TV server. Use the address of the machine on '
                        'your network, not "localhost".',
                        style: TextStyle(fontSize: context.ui(20)),
                      ),
                    ),
                    Padding(
                      padding: EdgeInsets.symmetric(horizontal: context.ui(24), vertical: context.ui(6)),
                      child: TextField(
                        key: const Key('server-url-field'),
                        controller: _url,
                        focusNode: _node,
                        autofocus: true,
                        autocorrect: false,
                        enableSuggestions: false,
                        keyboardType: TextInputType.url,
                        textInputAction: TextInputAction.done,
                        style: TextStyle(fontSize: context.ui(22)),
                        decoration: InputDecoration(
                          labelText: 'Playhead TV URL',
                          hintText: 'http://server.example.com:9394',
                          border: const OutlineInputBorder(),
                          labelStyle: TextStyle(fontSize: context.ui(20)),
                        ),
                        onChanged: (_) {
                          if (_error != null) setState(() => _error = null);
                        },
                        onSubmitted: (_) => unawaited(_continue()),
                      ),
                    ),
                    if (_error != null)
                      Padding(
                        padding: EdgeInsets.symmetric(horizontal: context.ui(24), vertical: context.ui(6)),
                        child: Text(_error!,
                            key: const Key('server-url-error'),
                            style: TextStyle(fontSize: context.ui(20), color: Colors.redAccent)),
                      ),
                    if (_probing)
                      Padding(
                        padding: EdgeInsets.all(context.ui(12)),
                        child: const Center(child: CircularProgressIndicator()),
                      ),
                    SizedBox(height: context.ui(12)),
                    Padding(
                      padding: EdgeInsets.symmetric(horizontal: context.ui(24)),
                      child: Row(children: [
                        FilledButton(
                          key: const Key('server-url-continue-button'),
                          onPressed: _probing ? null : () => unawaited(_continue()),
                          child: Padding(
                            padding: EdgeInsets.all(context.ui(10)),
                            child: Text('Continue', style: TextStyle(fontSize: context.ui(20))),
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

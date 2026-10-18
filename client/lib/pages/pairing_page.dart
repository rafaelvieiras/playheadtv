// Quick-connect pairing screen: POST /pair, show the temporary PIN large and a
// QR of <server>/pin?code=<PIN>, then poll /pair/poll on the interval the
// server asks for. On approval the credentials the server hands back are stored
// through the existing connection store and the app moves to the home page.
//
// A fresh PIN is requested when the current one expires or is already
// delivered. "Select another server" goes to ServerUrlPage; "Enter details
// manually" keeps the old SetupPage reachable. Six baseline buttons only,
// UiScale for every size, and no secret (PIN, poll secret or token) is ever
// logged.
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:http/http.dart' as http;
import 'package:qr_flutter/qr_flutter.dart';

import '../config/connection_settings.dart';
import '../input/input_action.dart';
import '../log/log.dart';
import '../main.dart' show AppConfig;
import '../services/device_identity.dart';
import '../services/pairing_boot.dart' show ServerUrlResolver;
import '../services/pairing_service.dart';
import '../services/settings_service.dart';
import '../ui/ui_scale.dart';
import 'mosaic_page.dart';
import 'server_url_page.dart';
import 'setup_page.dart';

/// Called after the credentials are stored, with the page's own context. The
/// boot flow in main.dart replaces itself with the home page here.
typedef PairingDone = void Function(BuildContext context);

class PairingPage extends StatefulWidget {
  /// The normalised backend URL the pairing talks to.
  final String serverUrl;
  final PairingDone? onDone;

  /// Replaced by tests (the API client uses it).
  final http.Client? httpClient;

  const PairingPage({super.key, required this.serverUrl, this.onDone, this.httpClient});

  @override
  State<PairingPage> createState() => _PairingPageState();
}

class _PairingPageState extends State<PairingPage> {
  SettingsService? _settings;
  KeyBindingsService? _keyBindings;
  PairingApi? _api;
  Timer? _timer;

  late String _serverUrl;
  String? _pin;
  String? _pollSecret;
  Duration _pollInterval = const Duration(seconds: 3);

  // Bumped on every restart/dispose so a late response from an old PIN can
  // never overwrite a newer one.
  int _generation = 0;
  bool _loading = true;
  bool _approved = false;
  String? _error;

  @override
  void initState() {
    super.initState();
    _serverUrl = ServerUrlResolver.normalize(widget.serverUrl);
    unawaited(_boot());
  }

  Future<void> _boot() async {
    final settings = await SettingsService.create();
    if (!mounted) return;
    setState(() {
      _settings = settings;
      _keyBindings = KeyBindingsService(settings);
    });
    _api = PairingApi(baseUrl: _serverUrl, client: widget.httpClient);
    await _startPairing();
  }

  Future<void> _startPairing() async {
    final api = _api;
    if (api == null) return;
    _timer?.cancel();
    final generation = ++_generation;
    setState(() {
      _loading = true;
      _error = null;
      _pin = null;
      _pollSecret = null;
    });
    final result = await api.start(name: DeviceIdentity.name(), platform: DeviceIdentity.platform());
    if (!mounted || generation != _generation) return;
    switch (result) {
      case final PairingStarted started:
        setState(() {
          _loading = false;
          _pin = started.pin;
          _pollSecret = started.pollSecret;
          _pollInterval = started.pollInterval;
        });
        // The PIN itself is never logged.
        Log.info('pairing', 'pairing code issued, expires in ${started.expiresIn.inSeconds}s');
        _schedulePoll(started.pollInterval);
      case final PairingStartFailure failure:
        setState(() {
          _loading = false;
          _error = failure.message;
        });
        Log.info('pairing', 'pairing start refused (${failure.statusCode ?? 'network'})');
    }
  }

  void _schedulePoll(Duration interval) {
    _timer?.cancel();
    _timer = Timer(interval, () => unawaited(_poll()));
  }

  Future<void> _poll() async {
    final api = _api;
    final secret = _pollSecret;
    if (api == null || secret == null) return;
    final generation = _generation;
    final result = await api.poll(secret, retryAfter: _pollInterval);
    if (!mounted || generation != _generation) return;
    switch (result) {
      case PairingPending(:final retryAfter):
        _pollInterval = retryAfter;
        _schedulePoll(retryAfter);
      case final PairingApproved approved:
        await _onApproved(approved);
      case PairingExpired():
        Log.info('pairing', 'pairing code expired, requesting a new one');
        await _startPairing();
      case final PairingPollFailure failure:
        // A transport hiccup, not an expiry: keep the PIN and poll again.
        Log.info('pairing', 'pairing poll failed (${failure.statusCode ?? 'network'})');
        _schedulePoll(_pollInterval);
    }
  }

  Future<void> _onApproved(PairingApproved approved) async {
    final settings = _settings;
    if (settings == null || _approved) return;
    _approved = true;
    _timer?.cancel();
    setState(() => _loading = true);
    final connection = ConnectionSettings(
      jellyfinUrl: approved.jellyfinUrl,
      jellyfinApiKey: approved.jellyfinApiKey,
      playheadBaseUrl: _serverUrl,
      playheadDeviceToken: approved.deviceToken,
    ).normalized();
    final problem = connection.validate();
    if (problem != null) {
      _approved = false;
      setState(() {
        _loading = false;
        _error = problem;
      });
      return;
    }
    await settings.setConnection(connection);
    await settings.setChosenServerUrl(_serverUrl);
    AppConfig.applyStoredConnection(connection);
    // No token or key is logged.
    Log.info('pairing', 'device approved, connection stored');
    if (!mounted) return;
    final onDone = widget.onDone;
    if (onDone != null) {
      onDone(context);
      return;
    }
    // Stand-in for the home page until main.dart wires this page in: the same
    // destination the current first-run setup flow uses.
    Navigator.of(context).pushReplacement(MaterialPageRoute(builder: (_) => const MosaicPage()));
  }

  void _goToServerUrl() {
    _timer?.cancel();
    Navigator.of(context).pushReplacement(MaterialPageRoute(
      builder: (_) => ServerUrlPage(httpClient: widget.httpClient),
    ));
  }

  void _goToManual() {
    _timer?.cancel();
    Navigator.of(context).pushReplacement(MaterialPageRoute(
      builder: (_) => SetupPage(
        firstRun: true,
        httpClient: widget.httpClient,
        onSaved: (ctx) => Navigator.of(ctx).pushReplacement(MaterialPageRoute(builder: (_) => const MosaicPage())),
      ),
    ));
  }

  @override
  void dispose() {
    _timer?.cancel();
    _generation++;
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => UiScaled(child: _buildPage(context));

  Widget _qr(BuildContext context) {
    final pin = _pin;
    if (pin == null) return const SizedBox.shrink();
    final data = '$_serverUrl/pin?code=${Uri.encodeQueryComponent(pin)}';
    return Center(
      child: Container(
        key: const Key('pairing-qr'),
        margin: EdgeInsets.symmetric(vertical: context.ui(12)),
        // White background plus padding gives the QR its required quiet zone.
        padding: EdgeInsets.all(context.ui(16)),
        decoration: BoxDecoration(color: Colors.white, borderRadius: BorderRadius.circular(context.ui(12))),
        child: QrImageView(
          data: data,
          version: QrVersions.auto,
          size: context.ui(200),
          errorCorrectionLevel: QrErrorCorrectLevel.M,
        ),
      ),
    );
  }

  Widget _buildPage(BuildContext context) {
    final pin = _pin;
    return Focus(
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
        key: const Key('pairing-page'),
        appBar: AppBar(
          title: const Text('Connect this device'),
          toolbarHeight: context.ui(kToolbarHeight),
        ),
        body: FocusTraversalGroup(
          policy: OrderedTraversalPolicy(),
          child: ListView(
            padding: EdgeInsets.symmetric(vertical: context.ui(12)),
            children: [
              if (_loading)
                Padding(
                  padding: EdgeInsets.all(context.ui(24)),
                  child: const Center(child: CircularProgressIndicator()),
                )
              else if (pin == null) ...[
                Padding(
                  padding: EdgeInsets.symmetric(horizontal: context.ui(24), vertical: context.ui(8)),
                  child: Text(_error ?? 'The server did not offer a pairing code.',
                      key: const Key('pairing-error'),
                      style: TextStyle(fontSize: context.ui(20), color: Colors.redAccent)),
                ),
                Padding(
                  padding: EdgeInsets.symmetric(horizontal: context.ui(24), vertical: context.ui(6)),
                  child: OutlinedButton(
                    key: const Key('pairing-retry-button'),
                    onPressed: () => unawaited(_startPairing()),
                    child: Padding(
                      padding: EdgeInsets.all(context.ui(10)),
                      child: Text('Try again', style: TextStyle(fontSize: context.ui(20))),
                    ),
                  ),
                ),
              ] else ...[
                Padding(
                  padding: EdgeInsets.symmetric(horizontal: context.ui(24), vertical: context.ui(8)),
                  child: Text('Enter this code on the Playhead TV website to approve this device.',
                      style: TextStyle(fontSize: context.ui(20))),
                ),
                Padding(
                  padding: EdgeInsets.symmetric(horizontal: context.ui(24), vertical: context.ui(8)),
                  child: Text(pin,
                      key: const Key('pairing-pin'),
                      textAlign: TextAlign.center,
                      style: TextStyle(
                          fontSize: context.ui(56), fontWeight: FontWeight.bold, letterSpacing: context.ui(6))),
                ),
                _qr(context),
                Padding(
                  padding: EdgeInsets.symmetric(horizontal: context.ui(24), vertical: context.ui(8)),
                  child: Text(
                    'On a phone or computer, open $_serverUrl/pin and enter the code. Sign in with your '
                    'Playhead TV account and approve this device.',
                    key: const Key('pairing-instructions'),
                    textAlign: TextAlign.center,
                    style: TextStyle(fontSize: context.ui(20)),
                  ),
                ),
              ],
              SizedBox(height: context.ui(12)),
              Padding(
                padding: EdgeInsets.symmetric(horizontal: context.ui(24)),
                child: Wrap(spacing: context.ui(16), runSpacing: context.ui(12), children: [
                  OutlinedButton(
                    key: const Key('pairing-select-server-button'),
                    onPressed: _goToServerUrl,
                    child: Padding(
                      padding: EdgeInsets.all(context.ui(10)),
                      child: Text('Select another server', style: TextStyle(fontSize: context.ui(20))),
                    ),
                  ),
                  OutlinedButton(
                    key: const Key('pairing-manual-button'),
                    onPressed: _goToManual,
                    child: Padding(
                      padding: EdgeInsets.all(context.ui(10)),
                      child: Text('Enter details manually', style: TextStyle(fontSize: context.ui(20))),
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

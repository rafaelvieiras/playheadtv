// The on-screen side of update_notices.dart: the "new version" banner and the
// one-time "updated to X" changelog, drawn over whatever page is on top.
//
// Rules this widget keeps:
//  - It never interrupts playback and never rebuilds a page: it sits above the
//    Navigator (MaterialApp.builder), the Navigator is passed through as the
//    ValueListenableBuilder's `child` and is never reconstructed, and nothing
//    here calls setState (see the GL-texture gotcha in AGENTS.md).
//  - It does NOT take focus. While it is visible, an early key handler claims
//    only the keys it needs, so the page below keeps focus and everything it
//    does not claim reaches it untouched:
//      Select (InputAction.select)  dismisses; a banner is then not shown again
//                                   until a newer release exists
//      Back (InputAction.back)      closes it (a banner returns on the next start)
//      Up / Down (raw arrows)       scroll a long changelog, and ONLY while it
//                                   scrolls; otherwise they go to the page
//    The six baseline buttons are all it ever needs.
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../input/input_action.dart';
import '../services/settings_service.dart';
import '../ui/ui_scale.dart';
import '../update/update_notices.dart';

/// Where the app gets its update-notice environment. Production uses the real
/// process environment; integration tests substitute their own.
class UpdateNotices {
  static UpdateNoticeEnvironment? _override;

  /// Tests only; pass null to restore the real environment.
  static void overrideForTest(UpdateNoticeEnvironment? environment) => _override = environment;

  static UpdateNoticeEnvironment environment() => _override ?? UpdateNoticeEnvironment.system();
}

class UpdateOverlayHost extends StatefulWidget {
  final Widget child;
  const UpdateOverlayHost({super.key, required this.child});

  @override
  State<UpdateOverlayHost> createState() => _UpdateOverlayHostState();
}

class _UpdateOverlayHostState extends State<UpdateOverlayHost> {
  final ValueNotifier<UpdateOverlay?> _view = ValueNotifier<UpdateOverlay?>(null);
  final ValueNotifier<bool> _scrollable = ValueNotifier<bool>(false);
  final ScrollController _scroll = ScrollController();
  UpdateNoticeController? _controller;
  KeyBindingsService? _bindings;
  bool _handlerAdded = false;

  @override
  void initState() {
    super.initState();
    _init();
  }

  Future<void> _init() async {
    try {
      final settings = await SettingsService.create();
      if (!mounted) return;
      _bindings = KeyBindingsService(settings);
      final controller = UpdateNoticeController(environment: UpdateNotices.environment(), store: settings);
      _controller = controller;
      controller.state.addListener(() {
        _view.value = controller.state.value;
        _scrollable.value = false;
        if (controller.state.value?.kind == UpdateOverlayKind.changelog) _measureScroll();
      });
      FocusManager.instance.addEarlyKeyEventHandler(_onKey);
      _handlerAdded = true;
      await controller.start();
    } on Object {
      // Update notices must never disturb the app.
    }
  }

  // Whether the changelog is longer than its box, known after its first layout.
  void _measureScroll() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      _scrollable.value = _scroll.hasClients && _scroll.position.maxScrollExtent > 0;
    });
  }

  KeyEventResult _onKey(KeyEvent event) {
    final overlay = _view.value;
    final controller = _controller;
    if (overlay == null || controller == null) return KeyEventResult.ignored;
    final key = event.logicalKey;

    // Menu-style navigation reads the raw arrows (AGENTS.md: it is not
    // remappable); they only scroll a changelog that does.
    if (key == LogicalKeyboardKey.arrowUp || key == LogicalKeyboardKey.arrowDown) {
      if (overlay.kind != UpdateOverlayKind.changelog || !_scrollable.value || !_scroll.hasClients) {
        return KeyEventResult.ignored;
      }
      if (event is! KeyUpEvent) {
        final step = 80.0 * (mounted ? UiScale.of(context) : 1.0);
        final delta = key == LogicalKeyboardKey.arrowDown ? step : -step;
        final target = (_scroll.offset + delta).clamp(0.0, _scroll.position.maxScrollExtent);
        _scroll.animateTo(target, duration: const Duration(milliseconds: 80), curve: Curves.easeOut);
      }
      return KeyEventResult.handled;
    }

    final action =
        _bindings?.resolve(key) ??
        (selectKeyAliases.contains(key)
            ? InputAction.select
            : backKeyAliases.contains(key)
            ? InputAction.back
            : null);
    if (action != InputAction.select && action != InputAction.back) return KeyEventResult.ignored;
    if (event is KeyDownEvent) {
      if (action == InputAction.select) {
        controller.select();
      } else {
        controller.close();
      }
    }
    // The matching repeat/up of a key this overlay took must not reach the page
    // either.
    return KeyEventResult.handled;
  }

  @override
  void dispose() {
    if (_handlerAdded) FocusManager.instance.removeEarlyKeyEventHandler(_onKey);
    _controller?.dispose();
    _view.dispose();
    _scrollable.dispose();
    _scroll.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return ValueListenableBuilder<UpdateOverlay?>(
      valueListenable: _view,
      child: widget.child,
      builder: (context, overlay, child) => Stack(
        children: [
          child!,
          if (overlay != null)
            overlay.kind == UpdateOverlayKind.banner ? _banner(context, overlay) : _changelog(context, overlay),
        ],
      ),
    );
  }

  static const _panelColor = Color(0xEB14141C);
  // Sizes are pixels at 1080p scaled by the shared UiScale (the host sits below
  // UiScaleHost, so `context.ui` is available; it subscribes only the builder).
  TextStyle _hintStyle(BuildContext context) => TextStyle(fontSize: context.ui(18), color: Colors.white60);

  Widget _banner(BuildContext context, UpdateOverlay overlay) {
    return Positioned(
      top: context.ui(24),
      left: context.ui(24),
      right: context.ui(24),
      child: Center(
        child: ConstrainedBox(
          constraints: BoxConstraints(maxWidth: context.ui(1100)),
          child: Material(
            key: const Key('update-banner'),
            color: _panelColor,
            borderRadius: BorderRadius.circular(context.ui(16)),
            child: Padding(
              padding: EdgeInsets.all(context.ui(22)),
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    overlay.title,
                    style: TextStyle(fontSize: context.ui(26), fontWeight: FontWeight.w600),
                  ),
                  SizedBox(height: context.ui(8)),
                  Text(
                    overlay.body,
                    key: const Key('update-banner-message'),
                    style: TextStyle(fontSize: context.ui(21)),
                  ),
                  SizedBox(height: context.ui(14)),
                  Text(
                    'Select: do not show this version again    Back: close',
                    key: Key('update-banner-hint'),
                    style: _hintStyle(context),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }

  Widget _changelog(BuildContext context, UpdateOverlay overlay) {
    final height = MediaQuery.sizeOf(context).height;
    return Positioned.fill(
      child: Center(
        child: ConstrainedBox(
          constraints: BoxConstraints(maxWidth: context.ui(1000), maxHeight: height * 0.75),
          child: Material(
            key: const Key('update-changelog'),
            color: _panelColor,
            borderRadius: BorderRadius.circular(context.ui(16)),
            child: Padding(
              padding: EdgeInsets.all(context.ui(28)),
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    overlay.title,
                    key: const Key('update-changelog-title'),
                    style: TextStyle(fontSize: context.ui(28), fontWeight: FontWeight.w600),
                  ),
                  SizedBox(height: context.ui(16)),
                  Flexible(
                    child: SingleChildScrollView(
                      controller: _scroll,
                      // Plain text, exactly as published: no markup, no links.
                      child: Text(
                        overlay.body,
                        key: const Key('update-changelog-text'),
                        style: TextStyle(fontSize: context.ui(21)),
                      ),
                    ),
                  ),
                  SizedBox(height: context.ui(16)),
                  ValueListenableBuilder<bool>(
                    valueListenable: _scrollable,
                    builder: (_, scrollable, _) => Text(
                      scrollable ? 'Up/Down: scroll    Select or Back: close' : 'Select or Back: close',
                      key: const Key('update-changelog-hint'),
                      style: _hintStyle(context),
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}

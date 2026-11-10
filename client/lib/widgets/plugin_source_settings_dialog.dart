// The settings one plugin source accepts, drawn from what the plugin declares
// (protocol 1, `config`; docs/PLUGINS.md "Declared settings").
//
// Nothing here knows a plugin's keys: a `choice` cycles through the values the
// plugin offers, a `bool` toggles, and `number`/`text` are typed. Changing one
// saves it straight away, because a settings screen that needs a "save" step has
// to decide what a half-filled form means — and on a TV remote that decision is
// worse than an immediate write.
//
// Six buttons: Up/Down move between the settings, Select changes the focused one
// (or opens the keyboard for a number/text), Back closes.
import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../input/input_action.dart';
import '../plugin/plugin_manifest.dart';
import '../ui/ui_scale.dart';
import 'app_icons.dart';

/// Opens the settings of one plugin source. [save] performs the write and
/// returns the source as the backend now holds it, so the screen shows what was
/// stored rather than what was asked for.
Future<void> showPluginSourceSettings(
  BuildContext context, {
  required PluginSource source,
  required List<PluginConfigField> fields,
  required Future<PluginSource> Function(Map<String, Object?> settings) save,
  required KeyBindingsService keyBindings,
}) {
  return showDialog<void>(
    context: context,
    builder: (_) => PluginSourceSettingsDialog(
      source: source,
      fields: fields,
      save: save,
      keyBindings: keyBindings,
    ),
  );
}

class PluginSourceSettingsDialog extends StatefulWidget {
  const PluginSourceSettingsDialog({
    super.key,
    required this.source,
    required this.fields,
    required this.save,
    required this.keyBindings,
  });

  final PluginSource source;
  final List<PluginConfigField> fields;
  final Future<PluginSource> Function(Map<String, Object?> settings) save;
  final KeyBindingsService keyBindings;

  @override
  State<PluginSourceSettingsDialog> createState() => _PluginSourceSettingsDialogState();
}

class _PluginSourceSettingsDialogState extends State<PluginSourceSettingsDialog> {
  late PluginSource _source = widget.source;
  bool _saving = false;
  String? _error;

  /// One node per setting, so the focus stays on the row being changed. Without
  /// it the list is rebuilt after every write and the focus falls away, which on
  /// a remote looks like the buttons stopped working.
  late final List<FocusNode> _nodes = [
    for (var i = 0; i < widget.fields.length; i++)
      FocusNode(debugLabel: 'plugin-setting-${widget.fields[i].key}'),
  ];
  int _focused = 0;

  @override
  void dispose() {
    for (final node in _nodes) {
      node.dispose();
    }
    super.dispose();
  }

  Future<void> _write(Object key, Object? value) async {
    setState(() {
      _saving = true;
      _error = null;
    });
    try {
      final updated = await widget.save({key.toString(): value});
      if (!mounted) return;
      setState(() => _source = updated);
    } on Object catch (e) {
      if (!mounted) return;
      setState(() => _error = 'Could not save: $e');
    } finally {
      if (mounted) {
        setState(() => _saving = false);
        if (_nodes.isNotEmpty) {
          _nodes[_focused.clamp(0, _nodes.length - 1)].requestFocus();
        }
      }
    }
  }

  /// What Select does on one setting: the next value a `choice` offers, the
  /// other state of a `bool`, and the keyboard for anything typed.
  Future<void> _activate(PluginConfigField field) async {
    // One write at a time. The tile stays enabled rather than being disabled
    // while it saves: a disabled ListTile is not focusable, so the focus would
    // fall off the row the user is changing and the next press would go nowhere.
    if (_saving) return;
    final current = _source.settings[field.key];
    switch (field.type) {
      case 'choice':
        final options = field.options;
        if (options.isEmpty) return;
        var index = options.indexWhere((o) => _sameValue(o.value, current));
        index = (index + 1) % options.length;
        await _write(field.key, options[index].value);
      case 'bool':
        await _write(field.key, current != true);
      case 'number':
        final text = await _askText(field, current == null ? '' : '$current', numeric: true);
        if (text == null) return;
        final parsed = int.tryParse(text.trim());
        if (parsed == null) {
          setState(() => _error = '${field.label} must be a whole number');
          return;
        }
        await _write(field.key, parsed);
      default:
        final text = await _askText(field, current == null ? '' : '$current');
        if (text == null) return;
        await _write(field.key, text.trim());
    }
  }

  Future<String?> _askText(PluginConfigField field, String initial, {bool numeric = false}) {
    final controller = TextEditingController(text: initial);
    return showDialog<String>(
      context: context,
      builder: (dialogContext) => AlertDialog(
        title: Text(field.label),
        content: TextField(
          key: const Key('plugin-setting-input'),
          controller: controller,
          autofocus: true,
          keyboardType: numeric ? TextInputType.number : TextInputType.text,
          decoration: InputDecoration(hintText: field.help),
          onSubmitted: (value) => Navigator.of(dialogContext).pop(value),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(dialogContext).pop(),
            child: const Text('Cancel'),
          ),
          TextButton(
            onPressed: () => Navigator.of(dialogContext).pop(controller.text),
            child: const Text('Save'),
          ),
        ],
      ),
    );
  }

  String _valueLabel(PluginConfigField field) => _source.settingLabel(field);

  @override
  Widget build(BuildContext context) {
    return Focus(
      canRequestFocus: false,
      skipTraversal: true,
      onKeyEvent: (node, event) {
        if (event is KeyDownEvent && widget.keyBindings.resolve(event.logicalKey) == InputAction.back) {
          Navigator.of(context).maybePop();
          return KeyEventResult.handled;
        }
        return KeyEventResult.ignored;
      },
      child: Dialog(
        key: const Key('plugin-source-settings'),
        child: ConstrainedBox(
          // A phone held sideways is 360 dp tall: 640 scaled would overflow, so
          // the box also stays inside 90% of the window.
          constraints: BoxConstraints(
            maxWidth: context.ui(720),
            maxHeight: math.min(context.ui(640), MediaQuery.sizeOf(context).height * 0.9),
          ),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Padding(
                padding: EdgeInsets.all(context.ui(16)),
                child: Text(
                  _source.name.isEmpty ? 'Plugin source' : _source.name,
                  style: Theme.of(context).textTheme.titleLarge,
                ),
              ),
              Flexible(
                child: ListView(
                  shrinkWrap: true,
                  children: [
                    for (var i = 0; i < widget.fields.length; i++)
                      _fieldTile(widget.fields[i], index: i),
                    if (_error != null)
                      ListTile(
                        key: const Key('plugin-settings-error'),
                        leading: const Icon(AppIcons.pluginMissing),
                        title: Text(_error!),
                      ),
                  ],
                ),
              ),
              Padding(
                padding: EdgeInsets.symmetric(horizontal: context.ui(16), vertical: context.ui(8)),
                child: Row(
                  children: [
                    if (_saving) ...[
                      const SizedBox(
                        width: 16,
                        height: 16,
                        child: CircularProgressIndicator(strokeWidth: 2),
                      ),
                      SizedBox(width: context.ui(8)),
                    ],
                    const Expanded(child: Text('Select changes a setting. Back returns.')),
                    TextButton(
                      onPressed: () => Navigator.of(context).maybePop(),
                      child: const Text('Close'),
                    ),
                  ],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }

  Widget _fieldTile(PluginConfigField field, {required int index}) {
    final help = field.help;
    return ListTile(
      key: Key('plugin-setting-${field.key}'),
      focusNode: _nodes[index],
      autofocus: index == 0,
      onFocusChange: (hasFocus) {
        if (hasFocus) _focused = index;
      },
      title: Text(field.label),
      subtitle: Text(
        help == null ? _valueLabel(field) : '${_valueLabel(field)} · $help',
        key: Key('plugin-setting-value-${field.key}'),
      ),
      trailing: const Icon(AppIcons.plugin),
      onTap: () => _activate(field),
    );
  }
}

bool _sameValue(Object? a, Object? b) {
  if (a is num && b is num) return a.toDouble() == b.toDouble();
  return a == b;
}

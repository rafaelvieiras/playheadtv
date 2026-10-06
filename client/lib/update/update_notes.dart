// The changelog of a release is the body of its GitHub Release: untrusted text
// (anyone who can edit a release can change it). It is only ever shown as plain
// text: never parsed as markup, never turned into links, bounded in size.

/// How much of a release body is kept (stored in state.json and shown).
const maxNotesLength = 4000;

/// Makes a release body safe to store and show as plain text: line endings
/// normalised, control characters (terminal escapes included) dropped, runs of
/// blank lines collapsed, trimmed, and cut to [maxLength] characters with an
/// ellipsis. Never throws.
String sanitizeNotes(String raw, {int maxLength = maxNotesLength}) {
  var text = raw.replaceAll('\r\n', '\n').replaceAll('\r', '\n').replaceAll('\t', '    ');
  text = text.replaceAll(RegExp(r'[\x00-\x09\x0B-\x1F\x7F-\x9F\u202A-\u202E\u2066-\u2069]'), '');
  text = text.replaceAll(RegExp(r'\n{3,}'), '\n\n').trim();
  if (text.length > maxLength) {
    // Do not cut a surrogate pair in half.
    var cut = maxLength;
    if (cut > 0 && (text.codeUnitAt(cut - 1) & 0xFC00) == 0xD800) cut--;
    text = '${text.substring(0, cut).trimRight()}\n...';
  }
  return text;
}

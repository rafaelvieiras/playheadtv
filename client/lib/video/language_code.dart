// Language tags as the rest of the app expects them. Stream rules (audio and
// subtitle language, `por`, `eng`, `pt*`) are authored against the three-letter
// tag a container carries, which is what libmdk reports. ExoPlayer (Media3)
// instead normalizes to the shortest ISO 639-1 form (`pt`, `en`, `pt-BR`), so
// its engine maps back here: the same file then resolves to the same track no
// matter which engine plays it.
//
// Three-letter codes follow the ISO 639-2/B ("bibliographic") variant, the one
// Matroska and mkvmerge write (`fre`, `ger`, `chi`), not the /T variant.

const Map<String, String> _twoToThree = {
  'ar': 'ara', 'bg': 'bul', 'bn': 'ben', 'bo': 'tib', 'ca': 'cat', 'cs': 'cze',
  'cy': 'wel', 'da': 'dan', 'de': 'ger', 'el': 'gre', 'en': 'eng', 'es': 'spa',
  'et': 'est', 'eu': 'baq', 'fa': 'per', 'fi': 'fin', 'fr': 'fre', 'he': 'heb',
  'hi': 'hin', 'hr': 'hrv', 'hu': 'hun', 'hy': 'arm', 'id': 'ind', 'is': 'ice',
  'it': 'ita', 'ja': 'jpn', 'ka': 'geo', 'ko': 'kor', 'lt': 'lit', 'lv': 'lav',
  'mi': 'mao', 'mk': 'mac', 'ms': 'may', 'my': 'bur', 'nb': 'nob', 'nl': 'dut',
  'no': 'nor', 'pl': 'pol', 'pt': 'por', 'ro': 'rum', 'ru': 'rus', 'sk': 'slo',
  'sl': 'slv', 'sq': 'alb', 'sr': 'srp', 'sv': 'swe', 'ta': 'tam', 'te': 'tel',
  'th': 'tha', 'tr': 'tur', 'uk': 'ukr', 'vi': 'vie', 'zh': 'chi',
};

// The /T spellings that differ from /B, folded onto /B.
const Map<String, String> _terminologicToBibliographic = {
  'bod': 'tib', 'ces': 'cze', 'cym': 'wel', 'deu': 'ger', 'ell': 'gre',
  'eus': 'baq', 'fas': 'per', 'fra': 'fre', 'hye': 'arm', 'isl': 'ice',
  'kat': 'geo', 'mkd': 'mac', 'mri': 'mao', 'msa': 'may', 'mya': 'bur',
  'nld': 'dut', 'ron': 'rum', 'slk': 'slo', 'sqi': 'alb', 'zho': 'chi',
};

/// Maps [tag] (`pt`, `pt-BR`, `por`, `deu`, ...) to the lowercase three-letter
/// ISO 639-2/B code a container carries. A region or script suffix is dropped.
/// An unknown tag is returned lowercased; an absent one, or `und`
/// (undetermined), is null: "no language", as libmdk reports it.
String? canonicalLanguage(String? tag) {
  if (tag == null) return null;
  final base = tag.trim().toLowerCase().split(RegExp('[-_]')).first;
  if (base.isEmpty || base == 'und') return null;
  return _twoToThree[base] ?? _terminologicToBibliographic[base] ?? base;
}

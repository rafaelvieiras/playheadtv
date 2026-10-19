import 'package:client/video/language_code.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  test('two-letter tags map to the three-letter code a container carries', () {
    expect(canonicalLanguage('pt'), 'por');
    expect(canonicalLanguage('en'), 'eng');
    expect(canonicalLanguage('ja'), 'jpn');
    expect(canonicalLanguage('es'), 'spa');
  });

  test('region and script suffixes are dropped, case ignored', () {
    expect(canonicalLanguage('pt-BR'), 'por');
    expect(canonicalLanguage('PT_br'), 'por');
    expect(canonicalLanguage('zh-Hant-TW'), 'chi');
  });

  test('the bibliographic variant is the canonical one', () {
    expect(canonicalLanguage('fr'), 'fre');
    expect(canonicalLanguage('fra'), 'fre');
    expect(canonicalLanguage('fre'), 'fre');
    expect(canonicalLanguage('de'), 'ger');
    expect(canonicalLanguage('deu'), 'ger');
  });

  test('a three-letter code and an unknown tag pass through lowercased', () {
    expect(canonicalLanguage('POR'), 'por');
    expect(canonicalLanguage('eng'), 'eng');
    expect(canonicalLanguage('tlh'), 'tlh');
  });

  test('absent, empty and undetermined mean no language', () {
    expect(canonicalLanguage(null), isNull);
    expect(canonicalLanguage(''), isNull);
    expect(canonicalLanguage('  '), isNull);
    expect(canonicalLanguage('und'), isNull);
  });
}

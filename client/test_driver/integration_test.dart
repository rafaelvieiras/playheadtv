// Host side of `flutter drive` for the performance suites, which have to run
// in profile mode (`flutter test` cannot): see tool/run_perf.sh.
import 'package:integration_test/integration_test_driver.dart';

Future<void> main() => integrationDriver();

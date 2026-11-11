// The phone shape of an action on a first-run screen: as wide as the content
// and at least a finger tall, which is what a thumb expects from the one thing
// a screen is asking for. Only the phone branch uses this widget; the
// television keeps its intrinsic-width buttons (see lib/ui/first_run_metrics.dart).
import 'package:flutter/material.dart';

import '../ui/first_run_metrics.dart';
import '../ui/form_factor.dart';

class FirstRunAction extends StatelessWidget {
  final Widget child;
  const FirstRunAction({super.key, required this.child});

  @override
  Widget build(BuildContext context) {
    final metrics = FirstRunMetrics.of(context.formFactor);
    return SizedBox(
      width: double.infinity,
      child: ConstrainedBox(
        constraints: BoxConstraints(minHeight: metrics.minActionHeight),
        child: child,
      ),
    );
  }
}

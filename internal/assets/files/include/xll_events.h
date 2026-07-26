#pragma once

namespace xll {
    void HandleCalculationEnded();

    // HandleCalculationCanceled forwards Excel's xleventCalculationCanceled to
    // the Go server as MSG_CALCULATION_CANCELED (132) so the user's
    // OnCalculationCanceled handler runs.
    //
    // It is emitted ONLY when the project declares `- type: CalculationCanceled`
    // in xll.yaml; nothing system-side depends on it (see the "no cache work"
    // note in the definition), so an undeclared project never registers the
    // event and never pays the round-trip.
    void HandleCalculationCanceled();
}

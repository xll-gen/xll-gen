#pragma once
// xll_topic.h — formatting helpers for RTD topic strings.
//
// An RTD topic is a vector of strings; the generated rtd / rtd-once wrappers
// build one per call from the function name plus the stringified scalar
// arguments, and rtd-once additionally joins them into its once-key
// (MakeRtdOnceKey, xll_rtd_once.h). The stringification therefore has to be
// injective over the argument values, which is a real constraint, not a
// formatting preference — see below.
//
// WHY IT LIVES HERE. This was inline in xll_main.cpp.tmpl with no template
// variables in it, so every project received a byte-identical re-emitted copy.
// It is header-only (one small function on the wrapper's hot path, called once
// per float/date argument per call) and NOT gated on XLL_RTD_ENABLED: it pulls
// in nothing but <cwchar>/<string>, and the generated TU includes it
// unconditionally so a config built directly in a generator test — which skips
// the "mode: rtd requires rtd.enabled" validation — still compiles.

#include <cwchar> // std::swprintf
#include <string>

namespace xll {

// FormatDoubleRoundTrip renders a double as a round-trippable decimal string for
// use as an RTD topic component (and, for rtd-once, the once-key). std::to_wstring
// (double) formats with %f — 6 fractional digits — which TRUNCATES precision: two
// distinct arguments (e.g. 1e-7 and 2e-7, both "0.000000") collapse to ONE topic
// string, merging their topics and colliding their memo/once-key entries. %.17g is
// the shortest format guaranteed to round-trip an IEEE-754 double, and Go's
// strconv.ParseFloat parses its output (including exponent notation) unchanged, so
// no Go-side change is required.
inline std::wstring FormatDoubleRoundTrip(double v) {
    wchar_t buf[32];
    std::swprintf(buf, sizeof(buf) / sizeof(buf[0]), L"%.17g", v);
    return std::wstring(buf);
}

} // namespace xll

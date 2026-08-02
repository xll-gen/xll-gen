package assets

import (
	"strings"
	"testing"
)

// FormatDoubleRoundTrip moved out of internal/templates/xll_main.cpp.tmpl into
// include/xll_topic.h on 2026-08-03 (it had no template variables, so every
// project was getting a byte-identical re-emitted copy that only a golden-string
// grep could test). This file holds the BODY invariants.
//
// WHERE EACH OLD ASSERTION WENT — both were in
// internal/generator/gen_escape_precision_test.go::TestGenCpp_RtdFloatTopicRoundTrip
// against renderCppMain():
//
//	`L"%.17g"`                                     -> TestFormatDoubleRoundTripUsesShortestRoundTrip
//	`std::wstring FormatDoubleRoundTrip(double v)` -> TestFormatDoubleRoundTripUsesShortestRoundTrip
//
// The four per-argument CALL SITES stay in the generator test: which argument
// types route through the helper is codegen, not library behavior. The generator
// test additionally pins the #include and carries a do-not-re-inline guard.

// TestFormatDoubleRoundTripUsesShortestRoundTrip pins the one thing about this
// function that is a correctness property rather than a formatting taste.
//
// An RTD topic is identified by its strings. std::to_wstring(double) formats with
// %f — six fractional digits — so 1e-7 and 2e-7 both render "0.000000" and two
// distinct calls COLLAPSE ONTO ONE TOPIC: they share a subscription, they share
// the rtd-once memo entry keyed by MakeRtdOnceKey, and one cell silently shows the
// other's value. %.17g is the shortest format guaranteed to round-trip an
// IEEE-754 double, and Go's strconv.ParseFloat accepts its output (exponent
// notation included) on the other side of the topic string.
func TestFormatDoubleRoundTripUsesShortestRoundTrip(t *testing.T) {
	t.Parallel()
	hdr := topicHeader(t)

	if !strings.Contains(hdr, `L"%.17g"`) {
		t.Errorf("xll_topic.h: FormatDoubleRoundTrip must format with %%.17g — %%f (std::to_wstring) " +
			"truncates to 6 fractional digits and merges distinct RTD topics")
	}
	// A relapse to the lossy formatter is the exact defect, so name it.
	if strings.Contains(stripCppCommentsAsset(hdr), "std::to_wstring") {
		t.Errorf("xll_topic.h must not fall back to std::to_wstring (that is the %%f truncation bug)")
	}

	// The definition, and its INLINE linkage: this is a header included by the
	// generated TU, so a non-inline definition is a duplicate-symbol error the
	// moment a second TU includes it.
	if !strings.Contains(hdr, "inline std::wstring FormatDoubleRoundTrip(double v) {") {
		t.Errorf("xll_topic.h must define `inline std::wstring FormatDoubleRoundTrip(double v)`")
	}
	// It lives in namespace xll, which is what lets the generated wrappers keep
	// calling it unqualified through their `using namespace xll;`.
	if !strings.Contains(hdr, "namespace xll {") {
		t.Errorf("xll_topic.h must put FormatDoubleRoundTrip in namespace xll (the generated wrappers " +
			"call it unqualified via `using namespace xll;`)")
	}

	// The buffer must be large enough for the widest %.17g rendering. Worst case
	// is sign + 17 significant digits + '.' + "e-308" + NUL = 26 wchar_t; 32 is
	// the shipped size and swprintf is given the ELEMENT count, not the byte
	// count (sizeof(buf)/sizeof(buf[0])) — passing sizeof(buf) would let it write
	// 32 wchar_t into a 32-wchar_t buffer's worth of BYTES.
	if !strings.Contains(hdr, "wchar_t buf[32];") {
		t.Errorf("xll_topic.h: the %%.17g scratch buffer must stay at 32 wchar_t")
	}
	if !strings.Contains(hdr, "sizeof(buf) / sizeof(buf[0])") {
		t.Errorf("xll_topic.h: swprintf takes the ELEMENT count; passing sizeof(buf) would double the " +
			"claimed capacity of a wchar_t buffer")
	}
}

// TestTopicHeaderIsNotRtdGated: xll_main.cpp includes this header
// UNCONDITIONALLY. Gating it on XLL_RTD_ENABLED (the way xll_rtd_once.h is)
// would break any render whose config declares an rtd/rtd-once function without
// rtd.enabled — unreachable through config.Validate, but generator tests build
// Config structs directly and skip validation, and the compile gates render from
// those. The header pulls in nothing but <cwchar>/<string>, so there is no cost
// to carrying it everywhere.
func TestTopicHeaderIsNotRtdGated(t *testing.T) {
	t.Parallel()
	hdr := topicHeader(t)
	for _, gone := range []string{"#ifdef XLL_RTD_ENABLED", "#ifndef XLL_RTD_ENABLED", "#error"} {
		if strings.Contains(hdr, gone) {
			t.Errorf("xll_topic.h must stay ungated (found %q): the generated TU includes it "+
				"unconditionally", gone)
		}
	}
	for _, want := range []string{"#include <cwchar>", "#include <string>"} {
		if !strings.Contains(hdr, want) {
			t.Errorf("xll_topic.h must be self-contained; missing %q", want)
		}
	}
}

func topicHeader(t *testing.T) string {
	t.Helper()
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	hdr, ok := m["include/xll_topic.h"]
	if !ok {
		t.Fatalf("embedded asset include/xll_topic.h not found")
	}
	return hdr
}

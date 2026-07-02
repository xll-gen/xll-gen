package xldate

import (
	"testing"
	"time"
)

func TestToSerial(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		want float64
	}{
		{"epoch", time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC), 0},
		{"leap-boundary", time.Date(1900, 3, 1, 0, 0, 0, 0, time.UTC), 61},
		{"modern", time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), 46188},
		{"noon", time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC), 46188.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ToSerial(c.in)
			if got != c.want {
				t.Fatalf("ToSerial(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestToSerial_WallClockIgnoresLocation(t *testing.T) {
	utc := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	kst := time.Date(2026, 6, 15, 9, 0, 0, 0, time.FixedZone("KST", 9*3600))
	if ToSerial(utc) != ToSerial(kst) {
		t.Fatalf("wall-clock serials differ: utc=%v kst=%v", ToSerial(utc), ToSerial(kst))
	}
}

func TestFromSerial_RoundTrip(t *testing.T) {
	in := time.Date(2026, 6, 15, 12, 30, 0, 0, time.UTC)
	got := FromSerial(ToSerial(in))
	// A single float64 serial cannot exactly represent both a large day count
	// and a sub-day time-of-day: at serial ~46188 the ULP is ~628ns, so an
	// intra-day time round-trips only to that precision. (The former exact
	// equality here was an artifact of the old symmetric multiply/divide — the
	// very operations that overflowed and corrupted every date past 2192.)
	// Assert round-trip to well within a microsecond instead.
	const tol = time.Microsecond
	if d := got.Sub(in); d < -tol || d > tol {
		t.Fatalf("round-trip = %v, want %v (delta %v > %v)", got, in, d, tol)
	}
}

// TestFromSerial_PastDurationCeiling pins the fix for the silent corruption of
// dates past 2192-04-09. Serial 106,752 is the first day past the old
// int64-nanosecond time.Duration ceiling (~292 years); pre-fix FromSerial
// packed the whole serial into one time.Duration, so 106,751 and 106,752 (and
// everything beyond) collapsed onto the same saturated 1607-09-20 instant.
func TestFromSerial_PastDurationCeiling(t *testing.T) {
	cases := []struct {
		serial float64
		want   time.Time
	}{
		{106751, time.Date(2192, 4, 8, 0, 0, 0, 0, time.UTC)},
		{106752, time.Date(2192, 4, 9, 0, 0, 0, 0, time.UTC)},
		// 9999-12-31 is Excel's max date (serial 2,958,465); it can arrive as the
		// perpetual-bond sentinel maturity.
		{2958465, time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got := FromSerial(c.serial)
		if !got.Equal(c.want) {
			t.Errorf("FromSerial(%v) = %v, want %v", c.serial, got, c.want)
		}
	}
	// The boundary pair must be distinct (pre-fix they were identical).
	if FromSerial(106751).Equal(FromSerial(106752)) {
		t.Errorf("FromSerial(106751) and FromSerial(106752) must differ (both saturated pre-fix)")
	}
}

// TestSerial_RoundTrip_Boundaries round-trips the boundary serials and the two
// documented edges (the 1900 leap boundary and Excel's max date) at day
// granularity, where the conversion is exact.
func TestSerial_RoundTrip_Boundaries(t *testing.T) {
	dates := []time.Time{
		time.Date(1900, 3, 1, 0, 0, 0, 0, time.UTC),   // leap-bug boundary (serial 61)
		time.Date(2192, 4, 8, 0, 0, 0, 0, time.UTC),   // serial 106751
		time.Date(2192, 4, 9, 0, 0, 0, 0, time.UTC),   // serial 106752 (past the old ceiling)
		time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC), // Excel max date
	}
	for _, d := range dates {
		if got := FromSerial(ToSerial(d)); !got.Equal(d) {
			t.Errorf("round-trip %v = %v", d, got)
		}
	}
}

// TestToSerial_NoSaturation asserts ToSerial no longer clamps at the
// time.Duration ceiling: distinct far-future dates must map to distinct,
// monotonically increasing serials.
func TestToSerial_NoSaturation(t *testing.T) {
	s51 := ToSerial(time.Date(2192, 4, 8, 0, 0, 0, 0, time.UTC))
	s52 := ToSerial(time.Date(2192, 4, 9, 0, 0, 0, 0, time.UTC))
	sMax := ToSerial(time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC))
	if s51 != 106751 || s52 != 106752 {
		t.Errorf("boundary serials wrong: got %v/%v want 106751/106752", s51, s52)
	}
	if !(s52 > s51 && sMax > s52) {
		t.Errorf("serials must increase monotonically: %v < %v < %v", s51, s52, sMax)
	}
	if sMax != 2958465 {
		t.Errorf("ToSerial(9999-12-31) = %v, want 2958465", sMax)
	}
}

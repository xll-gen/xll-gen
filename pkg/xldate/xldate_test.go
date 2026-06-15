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
	if !got.Equal(in) {
		t.Fatalf("round-trip = %v, want %v", got, in)
	}
}

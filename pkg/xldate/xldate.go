// Package xldate converts between Go time.Time and Excel date serial numbers.
//
// Excel has no date type: a date is a double (days since the 1899-12-30 epoch)
// plus a cell number format. Conversion is WALL-CLOCK — the time.Time's own
// displayed components (year/month/day, hour/min/sec) are read as-is, with NO
// timezone conversion, because Excel has no concept of a timezone.
//
// 1900 leap-year bug: Excel incorrectly treats 1900 as a leap year (serial 60 =
// the non-existent 1900-02-29). Using the 1899-12-30 epoch makes serials EXACT
// for all dates on/after 1900-03-01 (the phantom day absorbs the +1 offset),
// which covers every practical financial date. Dates before 1900-03-01 are
// off by one versus Excel; that boundary is documented and out of scope.
package xldate

import (
	"math"
	"time"
)

// excelEpoch is Excel serial 0 (1899-12-30 00:00:00).
var excelEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

// ToSerial converts t to an Excel serial using t's wall-clock components.
func ToSerial(t time.Time) float64 {
	wall := time.Date(t.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	return wall.Sub(excelEpoch).Seconds() / 86400.0
}

// FromSerial converts an Excel serial back to a time.Time (UTC location). The
// integer part is the date; the fractional part is the time of day.
func FromSerial(serial float64) time.Time {
	ns := int64(math.Round(serial * 86400.0 * 1e9))
	return excelEpoch.Add(time.Duration(ns))
}

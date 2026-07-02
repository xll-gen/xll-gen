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
//
// The whole-day count is computed from Unix-second arithmetic (int64 seconds,
// range ~292 billion years) rather than time.Time.Sub, whose result is a
// time.Duration (int64 nanoseconds) and saturates at ~292 years (serial
// ~106,751). Using Sub silently clamped every serial past 2192-04-09 to the
// ~106,751.99 ceiling. Only the sub-second remainder (always < 1s) rides a
// nanosecond quantity.
func ToSerial(t time.Time) float64 {
	wall := time.Date(t.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	secs := wall.Unix() - excelEpoch.Unix()
	return float64(secs)/86400.0 + float64(wall.Nanosecond())/1e9/86400.0
}

// FromSerial converts an Excel serial back to a time.Time (UTC location). The
// integer part is the date; the fractional part is the time of day.
//
// The date is advanced with AddDate(0, 0, days) — which walks the calendar and
// never routes through a time.Duration — so it is exact all the way to
// 9999-12-31 (serial 2,958,465). Only the intra-day fraction (< 24h, well
// inside the int64-ns Duration range) is added as a Duration. The previous
// implementation packed the ENTIRE serial into one time.Duration, so any serial
// past ~106,751 (2192-04-09) overflowed and silently corrupted the date
// (FromSerial(2958465) returned 1607-09-20).
func FromSerial(serial float64) time.Time {
	days := math.Floor(serial)
	frac := serial - days
	ns := int64(math.Round(frac * 86400.0 * 1e9))
	return excelEpoch.AddDate(0, 0, int(days)).Add(time.Duration(ns))
}

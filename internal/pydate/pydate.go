// Package pydate implements the subset of Python's datetime.date/datetime and
// dateutil.relativedelta semantics used by the simplifier.
package pydate

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Value is a Python date or datetime. Naive values are stored in UTC; aware datetimes keep
// their fixed offset.
type Value struct {
	T          time.Time
	IsDatetime bool
	Aware      bool
}

var isoRe = regexp.MustCompile(`^(\d{4})-?(\d{2})-?(\d{2})(?:.(\d{2})(?::?(\d{2})(?::?(\d{2})(?:[.,](\d+))?)?)?)?` +
	`(Z|[+-]\d{2}(?::?\d{2}(?::?\d{2}(?:\.\d+)?)?)?)?$`)

// FromISOFormat mirrors datetime.fromisoformat (Python 3.11+): ok is false on ValueError.
func FromISOFormat(s string) (Value, bool) {
	m := isoRe.FindStringSubmatch(s)
	if m == nil {
		return Value{}, false
	}
	atoi := func(x string) int {
		if x == "" {
			return 0
		}
		n, _ := strconv.Atoi(x)
		return n
	}
	year, month, day := atoi(m[1]), atoi(m[2]), atoi(m[3])
	hour, minute, second := atoi(m[4]), atoi(m[5]), atoi(m[6])
	frac := m[7]
	if len(frac) > 6 {
		frac = frac[:6]
	}
	for len(frac) < 6 && frac != "" {
		frac += "0"
	}
	micro := atoi(frac)
	if month < 1 || month > 12 || day < 1 || day > daysIn(year, month) || hour > 23 || minute > 59 || second > 59 || year < 1 {
		return Value{}, false
	}
	loc := time.UTC
	aware := false
	if tz := m[8]; tz != "" {
		aware = true
		if tz != "Z" {
			sign := 1
			if tz[0] == '-' {
				sign = -1
			}
			rest := tz[1:]
			digits := ""
			for _, r := range rest {
				if r >= '0' && r <= '9' {
					digits += string(r)
				} else if r == '.' {
					break
				}
			}
			oh, om, os := atoi(digits[0:2]), 0, 0
			if len(digits) >= 4 {
				om = atoi(digits[2:4])
			}
			if len(digits) >= 6 {
				os = atoi(digits[4:6])
			}
			if oh > 23 || om > 59 || os > 59 {
				return Value{}, false
			}
			loc = time.FixedZone("", sign*(oh*3600+om*60+os))
		}
	}
	t := time.Date(year, time.Month(month), day, hour, minute, second, micro*1000, loc)
	return Value{T: t, IsDatetime: true, Aware: aware}, true
}

func daysIn(year, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// Date mirrors datetime.date().
func (v Value) Date() Value {
	y, mo, d := v.T.Date()
	return Value{T: time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)}
}

// ToDatetime mirrors datetime(year=..., month=..., day=...) for a date (identity for datetimes).
func (v Value) ToDatetime() Value {
	if v.IsDatetime {
		return v
	}
	return Value{T: v.T, IsDatetime: true}
}

// String mirrors str(date) / str(datetime).
func (v Value) String() string {
	if !v.IsDatetime {
		return v.T.Format("2006-01-02")
	}
	s := v.T.Format("2006-01-02 15:04:05")
	if us := v.T.Nanosecond() / 1000; us != 0 {
		s += fmt.Sprintf(".%06d", us)
	}
	if v.Aware {
		_, off := v.T.Zone()
		sign := "+"
		if off < 0 {
			sign = "-"
			off = -off
		}
		s += fmt.Sprintf("%s%02d:%02d", sign, off/3600, (off%3600)/60)
		if off%60 != 0 {
			s += fmt.Sprintf(":%02d", off%60)
		}
	}
	return s
}

// Equal mirrors ==: values of different kinds (date vs datetime, naive vs aware) are unequal.
func (v Value) Equal(o Value) bool {
	if v.IsDatetime != o.IsDatetime || v.Aware != o.Aware {
		return false
	}
	return v.T.Equal(o.T)
}

// Compare mirrors <, == and > (ok is false when Python raises TypeError).
func (v Value) Compare(o Value) (int, bool) {
	if v.IsDatetime != o.IsDatetime || v.Aware != o.Aware {
		return 0, false
	}
	return v.T.Compare(o.T), true
}

// Weekday mirrors date.weekday() (Monday is 0).
func (v Value) Weekday() int { return (int(v.T.Weekday()) + 6) % 7 }

// AddDays adds a timedelta of days (and keeps the kind).
func (v Value) AddDays(days int) Value {
	v.T = v.T.AddDate(0, 0, days)
	return v
}

// Replace mirrors date.replace / datetime.replace for the given fields (-1 keeps a field).
func (v Value) Replace(month, day, hour, minute, second, microsecond int) Value {
	y, mo, d := v.T.Date()
	h, mi, s := v.T.Clock()
	us := v.T.Nanosecond() / 1000
	if month >= 0 {
		mo = time.Month(month)
	}
	if day >= 0 {
		d = day
	}
	if hour >= 0 {
		h = hour
	}
	if minute >= 0 {
		mi = minute
	}
	if second >= 0 {
		s = second
	}
	if microsecond >= 0 {
		us = microsecond
	}
	v.T = time.Date(y, mo, d, h, mi, s, us*1000, v.T.Location())
	return v
}

// RelDelta mirrors dateutil.relativedelta (relative fields only).
type RelDelta struct {
	Years, Months, Days, Hours, Minutes, Seconds, Microseconds int
}

func sign(x int) int {
	if x < 0 {
		return -1
	}
	return 1
}

// NewRelDelta mirrors relativedelta(years=..., months=..., weeks=..., days=..., hours=...,
// minutes=..., seconds=..., microseconds=...) including its normalization (_fix).
func NewRelDelta(years, months, weeks, days, hours, minutes, seconds, microseconds int) RelDelta {
	r := RelDelta{years, months, days + weeks*7, hours, minutes, seconds, microseconds}
	r.fix()
	return r
}

func (r *RelDelta) fix() {
	carry := func(v *int, limit int, next *int) {
		if abs(*v) > limit-1 {
			s := sign(*v)
			div, mod := (*v*s)/limit, (*v*s)%limit
			*v = mod * s
			*next += div * s
		}
	}
	carry(&r.Microseconds, 1000000, &r.Seconds)
	carry(&r.Seconds, 60, &r.Minutes)
	carry(&r.Minutes, 60, &r.Hours)
	carry(&r.Hours, 24, &r.Days)
	carry(&r.Months, 12, &r.Years)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func (r RelDelta) hasTime() bool {
	return r.Hours != 0 || r.Minutes != 0 || r.Seconds != 0 || r.Microseconds != 0
}

// Neg mirrors -relativedelta.
func (r RelDelta) Neg() RelDelta {
	return RelDelta{-r.Years, -r.Months, -r.Days, -r.Hours, -r.Minutes, -r.Seconds, -r.Microseconds}
}

// AddTo mirrors `value + relativedelta` (ok is false on ValueError/OverflowError).
func (r RelDelta) AddTo(v Value) (Value, bool) {
	if r.hasTime() && !v.IsDatetime {
		v = v.ToDatetime()
	}
	y, mo, d := v.T.Date()
	year := y + r.Years
	month := int(mo)
	if r.Months != 0 {
		month += r.Months
		if month > 12 {
			year++
			month -= 12
		} else if month < 1 {
			year--
			month += 12
		}
	}
	if year < 1 || year > 9999 {
		return Value{}, false
	}
	day := min(daysIn(year, month), d)
	h, mi, s := v.T.Clock()
	base := time.Date(year, time.Month(month), day, h, mi, s, v.T.Nanosecond(), v.T.Location())
	delta := time.Duration(r.Hours)*time.Hour + time.Duration(r.Minutes)*time.Minute +
		time.Duration(r.Seconds)*time.Second + time.Duration(r.Microseconds)*time.Microsecond
	t := base.AddDate(0, 0, r.Days).Add(delta)
	if t.Year() < 1 || t.Year() > 9999 {
		return Value{}, false
	}
	return Value{T: t, IsDatetime: v.IsDatetime, Aware: v.Aware}, true
}

// SubFrom mirrors `value - relativedelta`.
func (r RelDelta) SubFrom(v Value) (Value, bool) { return r.Neg().AddTo(v) }

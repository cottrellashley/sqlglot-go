package executor

// Python's strftime and strptime for the directives sqlglot's time formats use.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// UTC is the location of timezone-aware UTC datetimes; naive datetimes use time.UTC.
var UTC = time.FixedZone("UTC", 0)

func isNaive(t time.Time) bool { return t.Location() == time.UTC }

var weekdays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// strftime mirrors datetime.strftime(format).
func strftime(t time.Time, format string) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			b.WriteByte(format[i])
			continue
		}
		i++
		switch format[i] {
		case 'Y':
			fmt.Fprintf(&b, "%04d", t.Year())
		case 'y':
			fmt.Fprintf(&b, "%02d", t.Year()%100)
		case 'm':
			fmt.Fprintf(&b, "%02d", int(t.Month()))
		case 'd':
			fmt.Fprintf(&b, "%02d", t.Day())
		case 'H':
			fmt.Fprintf(&b, "%02d", t.Hour())
		case 'I':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			fmt.Fprintf(&b, "%02d", h)
		case 'M':
			fmt.Fprintf(&b, "%02d", t.Minute())
		case 'S':
			fmt.Fprintf(&b, "%02d", t.Second())
		case 'f':
			fmt.Fprintf(&b, "%06d", t.Nanosecond()/1000)
		case 'j':
			fmt.Fprintf(&b, "%03d", t.YearDay())
		case 'p':
			if t.Hour() < 12 {
				b.WriteString("AM")
			} else {
				b.WriteString("PM")
			}
		case 'b':
			b.WriteString(t.Month().String()[:3])
		case 'B':
			b.WriteString(t.Month().String())
		case 'a':
			b.WriteString(weekdays[t.Weekday()][:3])
		case 'A':
			b.WriteString(weekdays[t.Weekday()])
		case 'w':
			b.WriteString(strconv.Itoa(int(t.Weekday())))
		case 'z':
			if !isNaive(t) {
				b.WriteString(t.Format("-0700"))
			}
		case 'Z':
			if !isNaive(t) {
				name, _ := t.Zone()
				b.WriteString(name)
			}
		case '%':
			b.WriteByte('%')
		default:
			b.WriteByte('%')
			b.WriteByte(format[i])
		}
	}
	return b.String()
}

// strptime mirrors datetime.strptime(value, format) for numeric directives and month names.
func strptime(value, format string) time.Time {
	year, month, day, hour, minute, second, micro := 1900, 1, 1, 0, 0, 0, 0
	pm, hasPM := false, false
	vi := 0
	fail := func() {
		panic(errValuef("time data %s does not match format %s", Repr(value), Repr(format)))
	}
	number := func(maxDigits int) int {
		start := vi
		for vi < len(value) && vi-start < maxDigits && value[vi] >= '0' && value[vi] <= '9' {
			vi++
		}
		if vi == start {
			fail()
		}
		n, _ := strconv.Atoi(value[start:vi])
		return n
	}
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			if vi >= len(value) || value[vi] != format[i] {
				fail()
			}
			vi++
			continue
		}
		i++
		switch format[i] {
		case 'Y':
			year = number(4)
		case 'y':
			year = number(2)
			if year < 69 {
				year += 2000
			} else {
				year += 1900
			}
		case 'm':
			month = number(2)
		case 'd':
			day = number(2)
		case 'H', 'I':
			hour = number(2)
		case 'M':
			minute = number(2)
		case 'S':
			second = number(2)
		case 'f':
			start := vi
			micro = number(6)
			for k := vi - start; k < 6; k++ {
				micro *= 10
			}
		case 'j':
			doy := number(3)
			t := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, doy-1)
			month, day = int(t.Month()), t.Day()
		case 'p':
			if vi+2 > len(value) {
				fail()
			}
			hasPM, pm = true, strings.EqualFold(value[vi:vi+2], "PM")
			vi += 2
		case 'b', 'B':
			matched := false
			for m := time.January; m <= time.December; m++ {
				for _, name := range []string{m.String(), m.String()[:3]} {
					if len(value)-vi >= len(name) && strings.EqualFold(value[vi:vi+len(name)], name) {
						month, vi, matched = int(m), vi+len(name), true
						break
					}
				}
				if matched {
					break
				}
			}
			if !matched {
				fail()
			}
		case '%':
			if vi >= len(value) || value[vi] != '%' {
				fail()
			}
			vi++
		default:
			panic(errValuef("'%c' is a bad directive in format %s", format[i], Repr(format)))
		}
	}
	if vi != len(value) {
		panic(errValuef("unconverted data remains: %s", value[vi:]))
	}
	if hasPM {
		hour %= 12
		if pm {
			hour += 12
		}
	}
	return time.Date(year, time.Month(month), day, hour, minute, second, micro*1000, time.UTC)
}

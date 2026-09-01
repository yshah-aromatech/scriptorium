// Package cron implements the scheduling engine with the exact semantics of
// the PowerShell app (src/Cron.psm1): vixie-cron field expansion including
// the dom/dow OR-rule, "5/15" = from-5-step-15, dow 0-7 with 7==sunday,
// month/day names, @keywords, and the frozen Next/Prev quirks (Next fires
// >= from+1min truncated to the minute; Prev looks back at most 35 days).
// Frozen against testdata/psfixtures/cron-truth.csv — change nothing here
// without changing the table's provenance.
package cron

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

var keywords = map[string]string{
	"@hourly": "0 * * * *", "@daily": "0 0 * * *", "@midnight": "0 0 * * *",
	"@weekly": "0 0 * * 0", "@monthly": "0 0 1 * *",
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *",
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

type schedule struct {
	minutes, hours, doms, months []int
	dows                         map[int]bool // normalized %7
	domRestricted, dowRestricted bool
}

// parse returns nil when the expression is invalid or @reboot (never fires).
func parse(expr string) *schedule {
	e := strings.ToLower(strings.TrimSpace(expr))
	if e == "@reboot" {
		return nil
	}
	if kw, ok := keywords[e]; ok {
		e = kw
	}
	f := strings.Fields(e)
	if len(f) != 5 {
		return nil
	}
	minutes := expandField(f[0], 0, 59, nil)
	hours := expandField(f[1], 0, 23, nil)
	doms := expandField(f[2], 1, 31, nil)
	months := expandField(f[3], 1, 12, monthNames)
	dowsRaw := expandField(f[4], 0, 7, dowNames)
	if minutes == nil || hours == nil || doms == nil || months == nil || dowsRaw == nil {
		return nil
	}
	dows := map[int]bool{}
	for _, d := range dowsRaw {
		dows[d%7] = true // 7 == sunday
	}
	return &schedule{
		minutes: minutes, hours: hours, doms: doms, months: months, dows: dows,
		// vixie day rule keys off the raw field text, not the expansion:
		// a field starting with '*' (including "*/2") is UNrestricted
		domRestricted: !strings.HasPrefix(f[2], "*"),
		dowRestricted: !strings.HasPrefix(f[4], "*"),
	}
}

// expandField ports ConvertFrom-StoCronField: sorted unique values, or nil
// on any parse failure.
func expandField(field string, min, max int, names map[string]int) []int {
	set := map[int]bool{}
	for _, part := range strings.Split(field, ",") {
		p := strings.ToLower(strings.TrimSpace(part))
		if p == "" {
			return nil
		}
		step := 1
		hasStep := false
		if idx := strings.LastIndex(p, "/"); idx >= 0 {
			// PS regex ^(.+)/(\d+)$ — the step must be all digits
			stepStr := p[idx+1:]
			if stepStr == "" {
				return nil
			}
			n, err := strconv.Atoi(stepStr)
			if err != nil || n < 1 {
				return nil
			}
			step = n
			hasStep = true
			p = p[:idx]
			if p == "" {
				return nil
			}
		}
		resolve := func(tok string) (int, bool) {
			if names != nil {
				if v, ok := names[tok]; ok {
					return v, true
				}
			}
			// PS gate is ^\d+$ — digits only, no signs
			for _, r := range tok {
				if r < '0' || r > '9' {
					return 0, false
				}
			}
			if tok == "" {
				return 0, false
			}
			v, err := strconv.Atoi(tok)
			if err != nil {
				return 0, false
			}
			return v, true
		}
		var lo, hi int
		switch {
		case p == "*":
			lo, hi = min, max
		case strings.Contains(p, "-"):
			lr := strings.SplitN(p, "-", 2)
			l, okL := resolve(lr[0])
			h, okH := resolve(lr[1])
			if !okL || !okH {
				return nil
			}
			lo, hi = l, h
		default:
			l, ok := resolve(p)
			if !ok {
				return nil
			}
			lo = l
			if hasStep {
				hi = max // "5/15": from 5, every 15, to max
			} else {
				hi = l
			}
		}
		if lo < min || hi > max || lo > hi {
			return nil
		}
		for v := lo; v <= hi; v += step {
			set[v] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]int, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

// Validate ports Test-StoCronExpression.
func Validate(expr string) bool {
	e := strings.TrimSpace(expr)
	switch strings.ToLower(e) {
	case "@hourly", "@daily", "@weekly", "@monthly", "@yearly", "@annually", "@reboot", "@midnight":
		return true
	}
	f := strings.Fields(strings.ToLower(e))
	if len(f) != 5 {
		return false
	}
	return expandField(f[0], 0, 59, nil) != nil &&
		expandField(f[1], 0, 23, nil) != nil &&
		expandField(f[2], 1, 31, nil) != nil &&
		expandField(f[3], 1, 12, monthNames) != nil &&
		expandField(f[4], 0, 7, dowNames) != nil
}

// Next ports Get-StoCronNext: first fire >= from+1min, truncated to the
// minute; the 1462-day scan covers every valid dom/month combination.
func Next(expr string, from time.Time) (time.Time, bool) {
	s := parse(expr)
	if s == nil {
		return time.Time{}, false
	}
	t := from.Add(time.Minute)
	t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, from.Location())
	day0 := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, from.Location())
	for i := 0; i < 1462; i++ {
		d := day0.AddDate(0, 0, i)
		if !contains(s.months, int(d.Month())) {
			continue
		}
		domOK := contains(s.doms, d.Day())
		dowOK := s.dows[int(d.Weekday())]
		var dayOK bool
		switch {
		case s.domRestricted && s.dowRestricted:
			dayOK = domOK || dowOK // the vixie OR-rule
		case s.domRestricted:
			dayOK = domOK
		case s.dowRestricted:
			dayOK = dowOK
		default:
			dayOK = true
		}
		if !dayOK {
			continue
		}
		startH := 0
		if i == 0 {
			startH = t.Hour()
		}
		for _, h := range s.hours {
			if h < startH {
				continue
			}
			startM := 0
			if i == 0 && h == t.Hour() {
				startM = t.Minute()
			}
			for _, mi := range s.minutes {
				if mi < startM {
					continue
				}
				return d.Add(time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute), true
			}
		}
	}
	return time.Time{}, false
}

// Prev ports Get-StoCronPrev: latest fire <= from, found by walking Next
// forward from a widening lookback (1h, 1d, 8d, 35d). Beyond 35 days the
// answer is "none" — a frozen quirk the truth table pins.
func Prev(expr string, from time.Time) (time.Time, bool) {
	for _, lookbackHours := range []int{1, 24, 24 * 8, 24 * 35} {
		t := from.Add(-time.Duration(lookbackHours) * time.Hour)
		var prev time.Time
		found := false
		for i := 0; i < 2000; i++ {
			n, ok := Next(expr, t)
			if !ok || n.After(from) {
				break
			}
			prev, found = n, true
			t = n
		}
		if found {
			return prev, true
		}
	}
	return time.Time{}, false
}

func contains(sorted []int, v int) bool {
	for _, x := range sorted {
		if x == v {
			return true
		}
		if x > v {
			return false
		}
	}
	return false
}

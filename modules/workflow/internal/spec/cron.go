package spec

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronSchedule is a parsed standard 5-field cron expression (minute hour
// day-of-month month day-of-week), evaluated in local time (design §7).
type cronSchedule struct {
	minute, hour, dom, month, dow map[int]bool
	// domStar/dowStar drive the standard OR rule: when both day fields are
	// restricted, either matching day fires.
	domStar, dowStar bool
}

// Numeric field bounds per position (minute hour dom month dow); dow accepts
// 0-7 with 7 folded onto Sunday.
var cronFieldBounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

// parseCron parses a standard 5-field cron expression; names (JAN, MON) and
// seconds fields are not supported.
func parseCron(expr string) (cronSchedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return cronSchedule{}, fmt.Errorf("cron expression needs 5 fields, got %d", len(fields))
	}
	var s cronSchedule
	sets := [...]*map[int]bool{&s.minute, &s.hour, &s.dom, &s.month, &s.dow}
	for i, raw := range fields {
		set, star, err := parseCronField(raw, cronFieldBounds[i][0], cronFieldBounds[i][1])
		if err != nil {
			return cronSchedule{}, fmt.Errorf("cron field %d (%q): %w", i+1, raw, err)
		}
		*sets[i] = set
		if i == 2 {
			s.domStar = star
		}
		if i == 4 {
			s.dowStar = star
		}
	}
	// Normalize 7 (alternate Sunday) onto 0.
	if s.dow[7] {
		s.dow[0] = true
		delete(s.dow, 7)
	}
	return s, nil
}

// parseCronField parses one field: `*`, numbers, ranges `a-b` and steps
// `*/s` / `a-b/s` / `a/s`, comma-separated. star reports whether the whole
// field is a literal `*` (unrestricted — the dom/dow OR rule keys on it).
func parseCronField(raw string, min, max int) (map[int]bool, bool, error) {
	set := map[int]bool{}
	for _, term := range strings.Split(raw, ",") {
		if term == "" {
			return nil, false, fmt.Errorf("empty term")
		}
		rangePart, stepPart, hasStep := strings.Cut(term, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepPart)
			if err != nil || n < 1 {
				return nil, false, fmt.Errorf("invalid step %q", stepPart)
			}
			step = n
		}
		lo, hi := min, max
		if rangePart != "*" {
			a, rest, _ := strings.Cut(rangePart, "-")
			n, err := strconv.Atoi(a)
			if err != nil || n < min || n > max {
				return nil, false, fmt.Errorf("value %q out of range [%d,%d]", a, min, max)
			}
			lo = n
			switch {
			case rest == "" && !hasStep:
				hi = lo
			case rest == "":
				hi = max // `a/s` means a-max/s
			default:
				b, err := strconv.Atoi(rest)
				if err != nil || b < lo || b > max {
					return nil, false, fmt.Errorf("invalid range %q", rangePart)
				}
				hi = b
			}
		}
		for v := lo; v <= hi; v += step {
			set[v] = true
		}
	}
	if len(set) == 0 {
		return nil, false, fmt.Errorf("field matches no value")
	}
	return set, raw == "*", nil
}

// dayMatches applies the month check and the dom/dow OR rule.
func (s cronSchedule) dayMatches(t time.Time) bool {
	if !s.month[int(t.Month())] {
		return false
	}
	domOK := s.dom[t.Day()]
	dowOK := s.dow[int(t.Weekday())]
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dowOK
	case s.dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}

// maxDayScan bounds the day search: four years cover every Feb 29 cycle, so
// exhausting it proves the schedule can never fire (e.g. Feb 30).
const maxDayScan = 4 * 366

// next returns the next fire time strictly after `after`, in after's
// location.
func (s cronSchedule) next(after time.Time) (time.Time, bool) {
	loc := after.Location()
	t := time.Date(after.Year(), after.Month(), after.Day(), after.Hour(), after.Minute()+1, 0, 0, loc)
	for i := 0; i < maxDayScan; i++ {
		if s.dayMatches(t) {
			if cursor, ok := s.nextMinuteOfDay(t); ok {
				return cursor, true
			}
		}
		t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	}
	return time.Time{}, false
}

// nextMinuteOfDay scans t's remaining day for the first minute matching the
// hour/minute sets.
func (s cronSchedule) nextMinuteOfDay(t time.Time) (time.Time, bool) {
	end := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).AddDate(0, 0, 1)
	for t.Before(end) {
		if s.hour[t.Hour()] && s.minute[t.Minute()] {
			return t, true
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, false
}

// NextFire returns the next fire time of a standard 5-field cron expression
// strictly after `after` (local timezone); ok=false when the expression is
// invalid or can never fire. Pure so the scheduler stays trivially testable
// (design §7).
func NextFire(expr string, after time.Time) (time.Time, bool) {
	s, err := parseCron(expr)
	if err != nil {
		return time.Time{}, false
	}
	return s.next(after)
}

// ValidateCron checks that expr parses as a standard 5-field cron expression;
// nil means the trigger scheduler will be able to evaluate it.
func ValidateCron(expr string) error {
	_, err := parseCron(expr)
	return err
}

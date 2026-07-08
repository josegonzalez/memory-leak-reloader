// Package maintenance gates disruptive restarts to approved time windows. A
// restart that is warranted outside every window is deferred (re-queued) to the
// next opening rather than dropped. Windows are same-day (start < end);
// overnight wrap is intentionally unsupported for predictability.
package maintenance

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
)

// Window is a recurring allowed interval: a set of weekdays and a same-day
// [start, end) time range in a specific location.
type Window struct {
	Days  map[time.Weekday]bool
	Start int // minutes since midnight, inclusive
	End   int // minutes since midnight, exclusive
	Loc   *time.Location
}

// Spec is the structured Helm/flag form of a single window.
type Spec struct {
	Days     string // e.g. "Mon-Fri", "Mon,Wed,Fri", "*"
	Start    string // "HH:MM"
	End      string // "HH:MM"
	Timezone string // IANA name; empty => UTC
}

// Windows is an ordered set of allowed windows. An empty Windows means
// "always allowed".
type Windows []Window

// IsAllowed reports whether t falls inside any window. Empty Windows => true.
func (ws Windows) IsAllowed(t time.Time) bool {
	if len(ws) == 0 {
		return true
	}
	for _, w := range ws {
		if w.contains(t) {
			return true
		}
	}
	return false
}

// NextOpening returns the earliest time strictly after t at which some window
// opens. It is only meaningful when !IsAllowed(t). Returns zero if there are no
// windows (callers should treat empty as always-allowed and not call this).
func (ws Windows) NextOpening(t time.Time) time.Time {
	var best time.Time
	for _, w := range ws {
		if c := w.nextOpening(t); !c.IsZero() && (best.IsZero() || c.Before(best)) {
			best = c
		}
	}
	return best
}

func (w Window) contains(t time.Time) bool {
	lt := t.In(w.Loc)
	if !w.Days[lt.Weekday()] {
		return false
	}
	m := lt.Hour()*60 + lt.Minute()
	return m >= w.Start && m < w.End
}

func (w Window) nextOpening(t time.Time) time.Time {
	lt := t.In(w.Loc)
	for off := 0; off <= 7; off++ {
		day := lt.AddDate(0, 0, off)
		if !w.Days[day.Weekday()] {
			continue
		}
		opening := time.Date(day.Year(), day.Month(), day.Day(), w.Start/60, w.Start%60, 0, 0, w.Loc)
		if opening.After(t) {
			return opening
		}
	}
	return time.Time{}
}

// ParseSpecs builds Windows from the structured Helm/flag form.
func ParseSpecs(specs []Spec) (Windows, error) {
	var ws Windows
	for i, s := range specs {
		w, err := parseWindow(s.Days, s.Start, s.End, s.Timezone)
		if err != nil {
			return nil, fmt.Errorf("window %d: %w", i, err)
		}
		ws = append(ws, w)
	}
	return ws, nil
}

// ParseString parses the controller-wide flag form: one or more windows
// separated by ";", each "<days> <HH:MM>-<HH:MM> <timezone>", e.g.
// "Mon-Fri 09:00-17:00 America/New_York". Used only for the --maintenance-window
// CLI flag; per-workload windows come structured from the policy spec.
func ParseString(s string) (Windows, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var ws Windows
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Fields(part)
		if len(fields) != 3 {
			return nil, fmt.Errorf("window %q: want '<days> <HH:MM>-<HH:MM> <timezone>'", part)
		}
		startEnd := strings.SplitN(fields[1], "-", 2)
		if len(startEnd) != 2 {
			return nil, fmt.Errorf("window %q: time range must be HH:MM-HH:MM", part)
		}
		w, err := parseWindow(fields[0], startEnd[0], startEnd[1], fields[2])
		if err != nil {
			return nil, fmt.Errorf("window %q: %w", part, err)
		}
		ws = append(ws, w)
	}
	return ws, nil
}

// FromWindows builds Windows from a MemoryLeakPolicy's structured maintenance
// windows. Days, times, and the timezone shape are schema-validated; the only
// runtime failure is a timezone that does not resolve (no tzdata in CEL).
func FromWindows(ws []v1alpha1.MaintenanceWindow) (Windows, error) {
	if len(ws) == 0 {
		return nil, nil
	}
	specs := make([]Spec, 0, len(ws))
	for _, w := range ws {
		days := make([]string, 0, len(w.Days))
		for _, d := range w.Days {
			days = append(days, string(d))
		}
		specs = append(specs, Spec{
			Days:     strings.Join(days, ","),
			Start:    w.Start,
			End:      w.End,
			Timezone: w.Timezone,
		})
	}
	return ParseSpecs(specs)
}

func parseWindow(days, start, end, tz string) (Window, error) {
	d, err := parseDays(days)
	if err != nil {
		return Window{}, err
	}
	sm, err := parseHHMM(start)
	if err != nil {
		return Window{}, err
	}
	em, err := parseHHMM(end)
	if err != nil {
		return Window{}, err
	}
	if em <= sm {
		return Window{}, fmt.Errorf("end %s must be after start %s (overnight windows unsupported)", end, start)
	}
	loc := time.UTC
	if tz != "" && tz != "UTC" {
		loc, err = time.LoadLocation(tz)
		if err != nil {
			return Window{}, fmt.Errorf("invalid timezone %q: %w", tz, err)
		}
	}
	return Window{Days: d, Start: sm, End: em, Loc: loc}, nil
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// weekdayOrder lets us expand ranges like Mon-Fri deterministically.
var weekdayOrder = []time.Weekday{
	time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday,
}

func parseDays(s string) (map[time.Weekday]bool, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	out := map[time.Weekday]bool{}
	if s == "*" || s == "daily" || s == "all" || s == "everyday" {
		for _, wd := range weekdayOrder {
			out[wd] = true
		}
		return out, nil
	}
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if rng := strings.SplitN(tok, "-", 2); len(rng) == 2 {
			from, ok1 := weekdays[strings.TrimSpace(rng[0])]
			to, ok2 := weekdays[strings.TrimSpace(rng[1])]
			if !ok1 || !ok2 {
				return nil, fmt.Errorf("invalid day range %q", tok)
			}
			for _, wd := range expandRange(from, to) {
				out[wd] = true
			}
			continue
		}
		wd, ok := weekdays[tok]
		if !ok {
			return nil, fmt.Errorf("invalid day %q", tok)
		}
		out[wd] = true
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid days in %q", s)
	}
	return out, nil
}

// expandRange returns weekdays from `from` to `to` inclusive, wrapping the week.
func expandRange(from, to time.Weekday) []time.Weekday {
	var out []time.Weekday
	for i := 0; i < 7; i++ {
		wd := time.Weekday((int(from) + i) % 7)
		out = append(out, wd)
		if wd == to {
			break
		}
	}
	return out
}

func parseHHMM(s string) (int, error) {
	s = strings.TrimSpace(s)
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("invalid time %q (want HH:MM)", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// String renders windows for logging.
func (ws Windows) String() string {
	if len(ws) == 0 {
		return "always"
	}
	parts := make([]string, 0, len(ws))
	for _, w := range ws {
		var days []string
		for _, wd := range weekdayOrder {
			if w.Days[wd] {
				days = append(days, wd.String()[:3])
			}
		}
		parts = append(parts, fmt.Sprintf("%s %02d:%02d-%02d:%02d %s",
			strings.Join(days, ","), w.Start/60, w.Start%60, w.End/60, w.End%60, w.Loc))
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

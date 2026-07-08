package maintenance

import (
	"testing"
	"time"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
)

// weekdaysMonFri is the explicit day list the structured spec uses instead of
// the old "Mon-Fri" range sugar.
var weekdaysMonFri = []v1alpha1.Day{"Mon", "Tue", "Wed", "Thu", "Fri"}

func TestEmptyAlwaysAllowed(t *testing.T) {
	var ws Windows
	if !ws.IsAllowed(time.Now()) {
		t.Fatal("empty windows should always allow")
	}
}

func TestFromWindowsAndIsAllowed(t *testing.T) {
	ws, err := FromWindows([]v1alpha1.MaintenanceWindow{
		{Days: weekdaysMonFri, Start: "09:00", End: "17:00", Timezone: "America/New_York"},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	ny, _ := time.LoadLocation("America/New_York")

	// Wednesday 12:00 ET -> allowed.
	in := time.Date(2026, 1, 7, 12, 0, 0, 0, ny) // Jan 7 2026 is a Wednesday
	if !ws.IsAllowed(in) {
		t.Errorf("Wed noon ET should be allowed")
	}
	// Wednesday 18:00 ET -> outside.
	out := time.Date(2026, 1, 7, 18, 0, 0, 0, ny)
	if ws.IsAllowed(out) {
		t.Errorf("Wed 6pm ET should be outside window")
	}
	// Saturday -> outside.
	sat := time.Date(2026, 1, 10, 12, 0, 0, 0, ny)
	if ws.IsAllowed(sat) {
		t.Errorf("Saturday should be outside window")
	}
}

func TestNextOpening(t *testing.T) {
	ws, err := FromWindows([]v1alpha1.MaintenanceWindow{
		{Days: weekdaysMonFri, Start: "09:00", End: "17:00", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Friday 18:00 UTC -> next opening Monday 09:00 UTC.
	fri := time.Date(2026, 1, 9, 18, 0, 0, 0, time.UTC) // Jan 9 2026 is a Friday
	next := ws.NextOpening(fri)
	want := time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC) // following Monday
	if !next.Equal(want) {
		t.Errorf("nextOpening = %v want %v", next, want)
	}

	// Wednesday 06:00 UTC -> opening same day 09:00.
	wed := time.Date(2026, 1, 7, 6, 0, 0, 0, time.UTC)
	if got := ws.NextOpening(wed); !got.Equal(time.Date(2026, 1, 7, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("nextOpening same-day = %v", got)
	}
}

func TestParseSpecs(t *testing.T) {
	ws, err := ParseSpecs([]Spec{{Days: "*", Start: "00:00", End: "23:59", Timezone: "UTC"}})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ws.IsAllowed(time.Date(2026, 1, 10, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("daily window should allow Saturday")
	}
}

func TestFromWindowsOvernightRejected(t *testing.T) {
	_, err := FromWindows([]v1alpha1.MaintenanceWindow{
		{Days: []v1alpha1.Day{"Mon"}, Start: "22:00", End: "02:00", Timezone: "UTC"},
	})
	if err == nil {
		t.Fatal("overnight window should be rejected")
	}
}

func TestFromWindowsUnknownTimezone(t *testing.T) {
	// The one check that survives schema validation: a well-shaped IANA name that
	// does not resolve at runtime.
	_, err := FromWindows([]v1alpha1.MaintenanceWindow{
		{Days: []v1alpha1.Day{"Mon"}, Start: "09:00", End: "17:00", Timezone: "Mars/Phobos"},
	})
	if err == nil {
		t.Fatal("unknown timezone should be rejected")
	}
}

package controller

import (
	"testing"
	"time"

	"github.com/josegonzalez/memory-leak-reloader/internal/sampling"
)

func TestToSamplePoints(t *testing.T) {
	if got := toSamplePoints(nil); len(got) != 0 {
		t.Errorf("nil in should yield no points, got %#v", got)
	}

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	in := []sampling.Sample{
		{Time: base, WorkingSet: 100, Limit: 1000},
		{Time: base.Add(time.Minute), WorkingSet: 200, Limit: 1000},
		{Time: base.Add(2 * time.Minute), WorkingSet: 300, Limit: 1000},
	}
	out := toSamplePoints(in)
	if len(out) != len(in) {
		t.Fatalf("length mismatch: %d want %d", len(out), len(in))
	}
	for i := range in {
		if out[i].Time != in[i].Time || out[i].Bytes != in[i].WorkingSet {
			t.Errorf("point %d = %+v want time=%s bytes=%d", i, out[i], in[i].Time, in[i].WorkingSet)
		}
	}
}

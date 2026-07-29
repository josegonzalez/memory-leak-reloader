package notify

import (
	"strings"
	"testing"
	"time"
)

func risingSeries(n int, start, step int64) []SamplePoint {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	out := make([]SamplePoint, n)
	for i := range out {
		out[i] = SamplePoint{Time: base.Add(time.Duration(i) * time.Minute), Bytes: start + int64(i)*step}
	}
	return out
}

func TestRenderChart_TooFewPoints(t *testing.T) {
	if got := renderChart(nil, 0); got != "" {
		t.Errorf("nil series should render nothing, got %q", got)
	}
	if got := renderChart([]SamplePoint{}, 0); got != "" {
		t.Errorf("empty series should render nothing, got %q", got)
	}
	one := risingSeries(1, 700*1024*1024, 0)
	if got := renderChart(one, 0); got != "" {
		t.Errorf("single point should render nothing, got %q", got)
	}
}

func TestRenderChart_RisingSeries(t *testing.T) {
	points := risingSeries(16, 700*1024*1024, 16*1024*1024)
	chart := renderChart(points, 860*1024*1024)
	if chart == "" {
		t.Fatal("rising series should render a chart")
	}
	if !strings.Contains(chart, "┤") {
		t.Errorf("chart missing axis marks:\n%s", chart)
	}
	if !strings.Contains(chart, "700Mi") {
		t.Errorf("y-axis labels should be humanized (want 700Mi):\n%s", chart)
	}
	if !strings.Contains(chart, "working set over 15m0s") {
		t.Errorf("caption should carry the series span:\n%s", chart)
	}
	if !strings.Contains(chart, "threshold 860Mi") {
		t.Errorf("caption should carry the humanized threshold:\n%s", chart)
	}
}

func TestRenderChart_FlatSeries(t *testing.T) {
	points := risingSeries(10, 500*1024*1024, 0)
	chart := renderChart(points, 860*1024*1024)
	if chart == "" {
		t.Fatal("flat series should still render")
	}
	if !strings.Contains(chart, "500Mi") {
		t.Errorf("flat series should label its value:\n%s", chart)
	}
}

func TestRenderChart_ThresholdDash(t *testing.T) {
	chart := renderChart(risingSeries(5, 100*1024*1024, 1024*1024), 0)
	if !strings.Contains(chart, "threshold —") {
		t.Errorf("zero threshold should render as a dash in the caption:\n%s", chart)
	}
}

func TestRenderChart_LongSeriesBounded(t *testing.T) {
	points := risingSeries(3000, 100*1024*1024, 512*1024)
	chart := renderChart(points, 0)
	if chart == "" {
		t.Fatal("long series should downsample, not disappear")
	}
	if len(chart) > chartMaxChars {
		t.Errorf("chart too large for a Slack section: %d chars", len(chart))
	}
	for _, line := range strings.Split(chart, "\n") {
		if n := len([]rune(line)); n > chartMaxWidth+20 {
			t.Errorf("line exceeds bounded width (%d runes): %q", n, line)
		}
	}
}

func TestRenderChart_CodeFenceSafe(t *testing.T) {
	chart := renderChart(risingSeries(30, 700*1024*1024, 8*1024*1024), 860*1024*1024)
	if strings.Contains(chart, "`") {
		t.Errorf("chart output must not contain backticks (breaks the code fence):\n%s", chart)
	}
}

func TestRenderChart_Deterministic(t *testing.T) {
	points := risingSeries(40, 700*1024*1024, 4*1024*1024)
	if a, b := renderChart(points, 860*1024*1024), renderChart(points, 860*1024*1024); a != b {
		t.Errorf("chart output should be deterministic:\n%s\n---\n%s", a, b)
	}
}

package notify

import (
	"fmt"
	"time"

	"github.com/guptarohit/asciigraph"
)

// The height/width bounds keep the largest possible chart well under Slack's
// 3000-character section text limit.
const (
	chartHeight   = 8
	chartMaxWidth = 60
)

// renderChart renders the series as a unicode line chart for a Slack code
// block, or "" when there are fewer than 2 points.
func renderChart(points []SamplePoint, threshold int64) string {
	if len(points) < 2 {
		return ""
	}
	data := make([]float64, len(points))
	for i, p := range points {
		data[i] = float64(p.Bytes)
	}
	span := points[len(points)-1].Time.Sub(points[0].Time).Round(time.Second)
	opts := []asciigraph.Option{
		asciigraph.Height(chartHeight),
		asciigraph.YAxisValueFormatter(func(v float64) string { return humanizeBytes(int64(v)) }),
		asciigraph.Caption(fmt.Sprintf("working set over %s · threshold %s", span, thresholdText(threshold))),
	}
	// Width interpolates in both directions, so applying it unconditionally
	// would fabricate points on short series; use it only to downsample.
	if len(points) > chartMaxWidth {
		opts = append(opts, asciigraph.Width(chartMaxWidth))
	}
	return asciigraph.Plot(data, opts...)
}

package main

import (
	"context"
	"fmt"
	"html/template"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/lib/pq"
)

type worldPoint struct {
	X float64
	Z float64
}

// fetchMapPositions returns ground-plane positions of score events on a map.
// The engine logs vec3 as "x/height/z"; x and z span the battlefield.
func (s *apiServer) fetchMapPositions(ctx context.Context, mapName string, scoreTypes []string, serverFilter string) ([]worldPoint, error) {
	rows, err := s.db.QueryContext(ctx, `
		select e.params->>'player_location'
		from events e
		join games g on g.id = e.game_id
		where g.map_name = $1
		  and e.event_type = 'score'
		  and lower(coalesce(e.params->>'score_type', '')) = any($2)
		  and e.params ? 'player_location'
		  and ($3 = '' or g.server_id = nullif($3,'')::uuid)
		limit 40000
	`, mapName, pq.Array(scoreTypes), serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []worldPoint
	for rows.Next() {
		var location string
		if err := rows.Scan(&location); err != nil {
			return nil, err
		}
		if p, ok := parseWorldPoint(location); ok {
			points = append(points, p)
		}
	}
	return points, rows.Err()
}

// parseWorldPoint parses the engine's "x/height/z" vec3 log format into a
// ground-plane point. Shared by the live query above and the
// map_summary_mv-backed fast path (mapPage in insight_pages.go), which
// stores the same raw location strings pre-sampled in a jsonb array.
func parseWorldPoint(location string) (worldPoint, bool) {
	parts := strings.Split(location, "/")
	if len(parts) != 3 {
		return worldPoint{}, false
	}
	x, errX := strconv.ParseFloat(parts[0], 64)
	z, errZ := strconv.ParseFloat(parts[2], 64)
	if errX != nil || errZ != nil {
		return worldPoint{}, false
	}
	return worldPoint{X: x, Z: z}, true
}

// heatmapSVG bins world positions into a grid and renders a single-hue
// density map: one sequential ramp (opacity of the given hex on the dark
// panel), sqrt-scaled so a single hotspot doesn't wash out the rest. Each
// cell carries a native <title> tooltip with its event count.
func heatmapSVG(points []worldPoint, hex string) template.HTML {
	if len(points) < 20 {
		return ""
	}

	// Clip to the 1st..99th percentile so a stray out-of-world position
	// doesn't stretch the field into a sliver.
	xs := make([]float64, len(points))
	zs := make([]float64, len(points))
	for i, p := range points {
		xs[i], zs[i] = p.X, p.Z
	}
	sort.Float64s(xs)
	sort.Float64s(zs)
	pct := func(sorted []float64, q float64) float64 {
		idx := int(q * float64(len(sorted)-1))
		return sorted[idx]
	}
	minX, maxX := pct(xs, 0.01), pct(xs, 0.99)
	minZ, maxZ := pct(zs, 0.01), pct(zs, 0.99)
	if maxX-minX < 1 || maxZ-minZ < 1 {
		return ""
	}

	const longAxisCells = 44
	spanX, spanZ := maxX-minX, maxZ-minZ
	cell := math.Max(spanX, spanZ) / longAxisCells
	cols := int(spanX/cell) + 1
	rowsN := int(spanZ/cell) + 1

	counts := make([]int, cols*rowsN)
	maxCount := 0
	for _, p := range points {
		if p.X < minX || p.X > maxX || p.Z < minZ || p.Z > maxZ {
			continue
		}
		col := int((p.X - minX) / cell)
		row := int((maxZ - p.Z) / cell) // flip so larger z renders upward
		if col >= cols {
			col = cols - 1
		}
		if row >= rowsN {
			row = rowsN - 1
		}
		counts[row*cols+col]++
		if counts[row*cols+col] > maxCount {
			maxCount = counts[row*cols+col]
		}
	}
	if maxCount == 0 {
		return ""
	}

	const width = 720.0
	size := width / float64(cols)
	height := size * float64(rowsN)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" role="img" aria-label="event density heatmap">`, width, height+26)
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%.0f" height="%.0f" fill="#11161c" stroke="#2a323c"/>`, width, height)
	for row := 0; row < rowsN; row++ {
		for col := 0; col < cols; col++ {
			count := counts[row*cols+col]
			if count == 0 {
				continue
			}
			opacity := 0.16 + 0.84*math.Sqrt(float64(count)/float64(maxCount))
			fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s" fill-opacity="%.2f"><title>%d event(s)</title></rect>`,
				float64(col)*size, float64(row)*size, size+0.5, size+0.5, hex, opacity, count)
		}
	}
	// Legend: the same single-hue ramp, low to high.
	legendY := height + 8
	fmt.Fprintf(&b, `<text x="0" y="%.0f" fill="#8b96a3" font-size="11">low</text>`, legendY+9)
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&b, `<rect x="%.0f" y="%.0f" width="14" height="10" fill="%s" fill-opacity="%.2f"/>`,
			30+float64(i)*15, legendY, hex, 0.16+0.84*math.Sqrt(float64(i+1)/8))
	}
	fmt.Fprintf(&b, `<text x="%.0f" y="%.0f" fill="#8b96a3" font-size="11">high</text>`, float64(30+8*15+6), legendY+9)
	fmt.Fprintf(&b, `<text x="%.0f" y="%.0f" fill="#8b96a3" font-size="11" text-anchor="end">%d positions &middot; world coordinates</text>`, width, legendY+9, len(points))
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

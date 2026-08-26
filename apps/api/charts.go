package main

import (
	"fmt"
	"html/template"
	"strings"
)

// Chart palette validated for the dark panel surface (#181d23) with the dataviz
// six-checks validator: lightness band, chroma floor, CVD separation, contrast.
const (
	colorKills   = "#bc861f" // slot 1 gold
	colorInfo    = "#4a8fd4" // slot 2 blue
	colorScore   = "#2f9e68" // slot 3 green
	colorDeaths  = "#d05e55" // slot 4 red
	chartSurface = "#181b18"
	chartGrid    = "#30372f"
	chartMuted   = "#a3aca0"
	chartText    = "#f3f0e8"
)

type seriesPoint struct {
	Label  string
	Kills  int
	Deaths int
	Score  int
}

type barItem struct {
	Label string
	Value int
	URL   string
}

type dashboardMetricRow struct {
	ID          string
	DisplayName string
	Metric      string
	Value       string
	Detail      string
}

// niceStep returns a 1/2/5×10^k step so axis ticks land on clean numbers.
func niceStep(raw float64) float64 {
	if raw <= 0 {
		return 1
	}
	mag := 1.0
	for mag*10 <= raw {
		mag *= 10
	}
	for _, m := range []float64{1, 2, 5, 10} {
		if mag*m >= raw {
			return mag * m
		}
	}
	return mag * 10
}

// performanceChartSVG renders kills and deaths per round as two polylines on a
// fixed-viewBox SVG that scales with its container. No client-side scripting;
// hover detail comes from native SVG <title> tooltips.
func performanceChartSVG(points []seriesPoint) template.HTML {
	if len(points) == 0 {
		return `<div class="muted">No round data yet.</div>`
	}

	const (
		width   = 820.0
		height  = 230.0
		padLeft = 40.0
		padWide = 46.0
		padTop  = 14.0
		padBot  = 30.0
	)
	plotW := width - padLeft - padWide
	plotH := height - padTop - padBot

	maxValue := 1
	for _, p := range points {
		if p.Kills > maxValue {
			maxValue = p.Kills
		}
		if p.Deaths > maxValue {
			maxValue = p.Deaths
		}
	}
	const gridLines = 4
	step := niceStep(float64(maxValue) / gridLines)
	axisMax := step * gridLines

	x := func(i int) float64 {
		if len(points) == 1 {
			return padLeft + plotW/2
		}
		return padLeft + plotW*float64(i)/float64(len(points)-1)
	}
	y := func(value float64) float64 {
		return padTop + plotH*(1-value/axisMax)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" role="img" aria-label="Kills and deaths per round">`, width, height)

	for i := 0; i <= gridLines; i++ {
		value := step * float64(i)
		gy := y(value)
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1"/>`, padLeft, gy, width-padWide, gy, chartGrid)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="10" fill="%s" text-anchor="end">%s</text>`, padLeft-8, gy+3, chartMuted, formatAxisValue(value))
	}

	labelEvery := (len(points) + 5) / 6
	if labelEvery < 1 {
		labelEvery = 1
	}
	for i, p := range points {
		if i%labelEvery != 0 && i != len(points)-1 {
			continue
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="9" fill="%s" text-anchor="middle">%s</text>`,
			x(i), height-10, chartMuted, template.HTMLEscapeString(p.Label))
	}

	writeSeries := func(color string, value func(seriesPoint) int) {
		var line strings.Builder
		for i, p := range points {
			fmt.Fprintf(&line, "%.1f,%.1f ", x(i), y(float64(value(p))))
		}
		fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`, strings.TrimSpace(line.String()), color)
		for i, p := range points {
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="4" fill="%s" stroke="%s" stroke-width="2"><title>%s: %d</title></circle>`,
				x(i), y(float64(value(p))), color, chartSurface, template.HTMLEscapeString(p.Label), value(p))
		}
	}
	writeSeries(colorDeaths, func(p seriesPoint) int { return p.Deaths })
	writeSeries(colorKills, func(p seriesPoint) int { return p.Kills })

	// Selective direct labels: the endpoint of each series, in text ink, skipped
	// when the two lines converge (the legend and tooltips carry it instead).
	last := points[len(points)-1]
	yK := y(float64(last.Kills))
	yD := y(float64(last.Deaths))
	diff := yK - yD
	if diff < 0 {
		diff = -diff
	}
	if diff >= 12 {
		ex := x(len(points)-1) + 10
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="11" font-weight="700" fill="%s">%d</text>`, ex, yK+4, chartText, last.Kills)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="11" font-weight="700" fill="%s">%d</text>`, ex, yD+4, chartText, last.Deaths)
	}

	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// columnChartSVG renders a single-series column chart: bars capped at 24px
// wide with a 4px rounded data-end and a square baseline, hairline gridlines,
// clean ticks, and native <title> tooltips. One series, so no legend — the
// panel heading names it.
func columnChartSVG(items []barItem, color string, ariaLabel string) template.HTML {
	if len(items) == 0 {
		return `<div class="muted">No data yet.</div>`
	}

	const (
		width   = 820.0
		height  = 230.0
		padLeft = 40.0
		padWide = 14.0
		padTop  = 14.0
		padBot  = 30.0
	)
	plotW := width - padLeft - padWide
	plotH := height - padTop - padBot

	maxValue := 1
	for _, item := range items {
		if item.Value > maxValue {
			maxValue = item.Value
		}
	}
	const gridLines = 4
	step := niceStep(float64(maxValue) / gridLines)
	axisMax := step * gridLines

	slot := plotW / float64(len(items))
	barW := slot * 0.62
	if barW > 24 {
		barW = 24
	}
	if barW < 2 {
		barW = 2
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" role="img" aria-label="%s">`, width, height, template.HTMLEscapeString(ariaLabel))

	baseline := padTop + plotH
	for i := 0; i <= gridLines; i++ {
		value := step * float64(i)
		gy := baseline - plotH*value/axisMax
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1"/>`, padLeft, gy, width-padWide, gy, chartGrid)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="10" fill="%s" text-anchor="end">%s</text>`, padLeft-8, gy+3, chartMuted, formatAxisValue(value))
	}

	labelEvery := (len(items) + 7) / 8
	if labelEvery < 1 {
		labelEvery = 1
	}
	for i, item := range items {
		cx := padLeft + slot*(float64(i)+0.5)
		h := plotH * float64(item.Value) / axisMax
		if item.Value > 0 && h < 2 {
			h = 2
		}
		if h > 0 {
			r := 4.0
			if h < r {
				r = h
			}
			x0 := cx - barW/2
			fmt.Fprintf(&b, `<path d="M%.1f %.1f v%.1f q0 -%.1f %.1f -%.1f h%.1f q%.1f 0 %.1f %.1f v%.1f z" fill="%s"><title>%s: %d</title></path>`,
				x0, baseline, -(h - r), r, r, r, barW-2*r, r, r, r, h-r, color,
				template.HTMLEscapeString(item.Label), item.Value)
		}
		if i%labelEvery == 0 || i == len(items)-1 {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="9" fill="%s" text-anchor="middle">%s</text>`,
				cx, height-10, chartMuted, template.HTMLEscapeString(item.Label))
		}
	}

	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func formatAxisValue(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.1fk", v/1000)
	}
	if v == float64(int(v)) {
		return fmt.Sprintf("%d", int(v))
	}
	return fmt.Sprintf("%.1f", v)
}

func combatTrendHTML(points []seriesPoint) template.HTML {
	if len(points) == 0 {
		return `<div class="muted">No round data yet.</div>`
	}
	maxValue := 1
	for _, p := range points {
		if p.Kills > maxValue {
			maxValue = p.Kills
		}
		if p.Deaths > maxValue {
			maxValue = p.Deaths
		}
	}
	var b strings.Builder
	b.WriteString(`<div class="combat-trend">`)
	for _, p := range points {
		kills := 100 * p.Kills / maxValue
		deaths := 100 * p.Deaths / maxValue
		if p.Kills > 0 && kills < 4 {
			kills = 4
		}
		if p.Deaths > 0 && deaths < 4 {
			deaths = 4
		}
		fmt.Fprintf(&b, `<div class="combat-round" title="%s: %d kills / %d deaths"><div class="combat-label">%s</div><div class="combat-bars"><i class="kill" style="height:%d%%"></i><i class="death" style="height:%d%%"></i></div><div class="combat-values"><span>%d K</span><span>%d D</span></div></div>`,
			template.HTMLEscapeString(p.Label),
			p.Kills,
			p.Deaths,
			template.HTMLEscapeString(shortTrendLabel(p.Label)),
			kills,
			deaths,
			p.Kills,
			p.Deaths)
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

func shortTrendLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "Round"
	}
	if len(label) <= 14 {
		return label
	}
	return label[:14]
}

// donutChartSVG renders the kills/deaths split with the K/D ratio centered.
func donutChartSVG(kills int, deaths int) template.HTML {
	total := kills + deaths
	ratio := "—"
	if deaths > 0 {
		ratio = fmt.Sprintf("%.2f", float64(kills)/float64(deaths))
	} else if kills > 0 {
		ratio = fmt.Sprintf("%d.00", kills)
	}

	const (
		size   = 180.0
		radius = 66.0
		stroke = 22.0
	)
	circumference := 2 * 3.14159265 * radius
	killsArc := 0.0
	if total > 0 {
		killsArc = circumference * float64(kills) / float64(total)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" role="img" aria-label="Kill death ratio">`, size, size)
	arc := func(color string, length, offset float64, label string, value int) {
		if length < 0.5 {
			length = 0.5 // keep a sliver visible for extreme splits
		}
		fmt.Fprintf(&b, `<circle cx="%.0f" cy="%.0f" r="%.0f" fill="none" stroke="%s" stroke-width="%.0f" stroke-dasharray="%.2f %.2f" stroke-dashoffset="%.2f" transform="rotate(-90 %.0f %.0f)"><title>%s: %d</title></circle>`,
			size/2, size/2, radius, color, stroke, length, circumference-length, -offset, size/2, size/2, label, value)
	}
	const gap = 2.0 // surface gap between touching segments
	switch {
	case total == 0:
		fmt.Fprintf(&b, `<circle cx="%.0f" cy="%.0f" r="%.0f" fill="none" stroke="%s" stroke-width="%.0f"/>`, size/2, size/2, radius, chartGrid, stroke)
	case kills == 0:
		arc(colorDeaths, circumference, 0, "Deaths", deaths)
	case deaths == 0:
		arc(colorKills, circumference, 0, "Kills", kills)
	default:
		arc(colorKills, killsArc-gap, gap/2, "Kills", kills)
		arc(colorDeaths, circumference-killsArc-gap, killsArc+gap/2, "Deaths", deaths)
	}
	fmt.Fprintf(&b, `<text x="%.0f" y="%.0f" font-size="26" font-weight="700" fill="%s" text-anchor="middle">%s</text>`, size/2, size/2+2, chartText, ratio)
	fmt.Fprintf(&b, `<text x="%.0f" y="%.0f" font-size="10" fill="%s" text-anchor="middle" letter-spacing="2">K/D</text>`, size/2, size/2+22, chartMuted)
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// winLossDonutSVG mirrors donutChartSVG for round outcomes: wins in green
// against losses in red, with the win rate in the middle.
func winLossDonutSVG(wins int, losses int) template.HTML {
	total := wins + losses
	rate := "—"
	if total > 0 {
		rate = fmt.Sprintf("%d%%", int(float64(wins)*100/float64(total)+0.5))
	}

	const (
		size   = 180.0
		radius = 66.0
		stroke = 22.0
	)
	circumference := 2 * 3.14159265 * radius
	winsArc := 0.0
	if total > 0 {
		winsArc = circumference * float64(wins) / float64(total)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" role="img" aria-label="Round outcomes">`, size, size)
	arc := func(color string, length, offset float64, label string, value int) {
		if length < 0.5 {
			length = 0.5 // keep a sliver visible for extreme splits
		}
		fmt.Fprintf(&b, `<circle cx="%.0f" cy="%.0f" r="%.0f" fill="none" stroke="%s" stroke-width="%.0f" stroke-dasharray="%.2f %.2f" stroke-dashoffset="%.2f" transform="rotate(-90 %.0f %.0f)"><title>%s: %d</title></circle>`,
			size/2, size/2, radius, color, stroke, length, circumference-length, -offset, size/2, size/2, label, value)
	}
	const gap = 2.0
	switch {
	case total == 0:
		fmt.Fprintf(&b, `<circle cx="%.0f" cy="%.0f" r="%.0f" fill="none" stroke="%s" stroke-width="%.0f"/>`, size/2, size/2, radius, chartGrid, stroke)
	case wins == 0:
		arc(colorDeaths, circumference, 0, "Losses", losses)
	case losses == 0:
		arc(colorScore, circumference, 0, "Wins", wins)
	default:
		arc(colorScore, winsArc-gap, gap/2, "Wins", wins)
		arc(colorDeaths, circumference-winsArc-gap, winsArc+gap/2, "Losses", losses)
	}
	fmt.Fprintf(&b, `<text x="%.0f" y="%.0f" font-size="26" font-weight="700" fill="%s" text-anchor="middle">%s</text>`, size/2, size/2+2, chartText, rate)
	fmt.Fprintf(&b, `<text x="%.0f" y="%.0f" font-size="10" fill="%s" text-anchor="middle" letter-spacing="2">WIN RATE</text>`, size/2, size/2+22, chartMuted)
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// barListHTML renders a horizontal bar breakdown as plain HTML/CSS.
func barListHTML(items []barItem, color string) template.HTML {
	if len(items) == 0 {
		return `<div class="muted">No data yet.</div>`
	}
	maxValue := 1
	for _, item := range items {
		if item.Value > maxValue {
			maxValue = item.Value
		}
	}

	var b strings.Builder
	b.WriteString(`<div class="bars">`)
	for _, item := range items {
		percent := 100 * item.Value / maxValue
		if percent < 2 {
			percent = 2
		}
		labelTag := "span"
		labelAttrs := ""
		if item.URL != "" {
			labelTag = "a"
			labelAttrs = ` href="` + template.HTMLEscapeString(item.URL) + `"`
		}
		fmt.Fprintf(&b,
			`<div class="bar-row"><%s class="bar-label mono" title="%s"%s>%s</%s><div class="bar-track"><div class="bar-fill" style="width:%d%%;background:%s"></div></div><div class="bar-value">%d</div></div>`,
			labelTag, template.HTMLEscapeString(item.Label), labelAttrs, template.HTMLEscapeString(item.Label), labelTag, percent, color, item.Value)
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

func codeMapBars(rows []codeMapRow, color string) template.HTML {
	items := make([]barItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, barItem{Label: displayCode(row.Name), Value: row.Count})
	}
	return barListHTML(items, color)
}

// codeMapColumnChart renders a monthly usage trend as a column chart. Unlike
// codeMapBars, the Name field here is already a "YYYY-MM" label rather than a
// weapon/vehicle/map code, so it's used as-is instead of going through
// displayCode.
func codeMapColumnChart(rows []codeMapRow) template.HTML {
	items := make([]barItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, barItem{Label: row.Name, Value: row.Count})
	}
	return columnChartSVG(items, colorInfo, "Usage by month")
}

func codePlayerBars(rows []codePlayerRow, color string) template.HTML {
	items := make([]barItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, barItem{Label: row.DisplayName, Value: row.Count, URL: playerURL(row.ID, "all")})
	}
	return barListHTML(items, color)
}

func kitMetricBars(rows []kitMetricRow, metric string, color string) template.HTML {
	items := make([]barItem, 0, len(rows))
	for _, row := range rows {
		value := row.Pickups
		switch metric {
		case "users":
			value = row.Users
		case "rounds":
			value = row.Rounds
		case "kills":
			value = row.KillsByCarriers
		}
		items = append(items, barItem{Label: displayCode(row.Code), Value: value})
	}
	return barListHTML(items, color)
}

func mapHeatmapHTML(rows []mapMetaRow) template.HTML {
	if len(rows) == 0 {
		return `<div class="muted">No map data yet.</div>`
	}
	maxKillsPerRound := 1.0
	maxPlayers := 1
	for _, row := range rows {
		kpr := 0.0
		if row.Rounds > 0 {
			kpr = float64(row.Kills) / float64(row.Rounds)
		}
		if kpr > maxKillsPerRound {
			maxKillsPerRound = kpr
		}
		if row.Players > maxPlayers {
			maxPlayers = row.Players
		}
	}
	cell := func(label string, value string, percent int, class string) string {
		if percent < 4 {
			percent = 4
		}
		if percent > 100 {
			percent = 100
		}
		return fmt.Sprintf(`<div class="heat-cell %s" style="--heat:%d%%"><span>%s</span><strong>%s</strong></div>`,
			class, percent, template.HTMLEscapeString(label), template.HTMLEscapeString(value))
	}
	var b strings.Builder
	b.WriteString(`<div class="heatmap">`)
	for _, row := range rows {
		killsPerRound := 0.0
		if row.Rounds > 0 {
			killsPerRound = float64(row.Kills) / float64(row.Rounds)
		}
		winTotal := row.Team1Wins + row.Team2Wins
		winSkew := 0
		if winTotal > 0 {
			diff := row.Team1Wins - row.Team2Wins
			if diff < 0 {
				diff = -diff
			}
			winSkew = diff * 100 / winTotal
		}
		fmt.Fprintf(&b, `<a class="heat-row" href="%s"><div class="heat-title"><strong>%s</strong><span>%d rounds</span></div>%s%s%s%s</a>`,
			template.HTMLEscapeString(mapURL(row.Name)),
			template.HTMLEscapeString(displayCode(row.Name)),
			row.Rounds,
			cell("Kills / Round", fmt.Sprintf("%.1f", killsPerRound), int(killsPerRound*100/maxKillsPerRound), "hot"),
			cell("Players", fmt.Sprintf("%d", row.Players), row.Players*100/maxPlayers, "cool"),
			cell("Win Skew", fmt.Sprintf("%d%%", winSkew), winSkew, "warn"),
			cell("Avg Time", fmt.Sprintf("%ds", row.AvgDuration), row.AvgDuration*100/180, "time"))
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

func metricRowsHTML(rows []dashboardMetricRow) template.HTML {
	if len(rows) == 0 {
		return `<div class="muted">No player metrics yet.</div>`
	}
	var b strings.Builder
	b.WriteString(`<div class="metric-list">`)
	for _, row := range rows {
		fmt.Fprintf(&b, `<a class="metric-row" href="%s"><span>%s</span><strong>%s</strong><em>%s</em><small>%s</small></a>`,
			template.HTMLEscapeString(playerURL(row.ID, "all")),
			template.HTMLEscapeString(row.Metric),
			template.HTMLEscapeString(row.Value),
			template.HTMLEscapeString(row.DisplayName),
			template.HTMLEscapeString(row.Detail))
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

func roundKillBars(rows []roundRow) template.HTML {
	items := []barItem{}
	for i, row := range rows {
		if i >= 14 {
			break
		}
		label := fmt.Sprintf("#%d %s", row.RoundIndex, row.EndedAt)
		items = append(items, barItem{Label: label, Value: row.Kills})
	}
	return barListHTML(items, colorKills)
}

package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type screen int

const (
	dashboard screen = iota
	advMenu
	advLimits
	advSpikes
	advRent
	advSessions
	advTimeline
	advDrift
)

var menu = []struct {
	screen      screen
	name, about string
}{
	{advLimits, "Limit forensics", "Who used your Codex weekly limit, and how many tokens 1% buys"},
	{advSpikes, "Spike explainer", "What made a day use more tokens than usual"},
	{advRent, "Context rent", "Tool outputs that cost the most because later calls sent them again"},
	{advSessions, "Session timeline", "Context size through one session, with cache rebuilds and compactions"},
	{advDrift, "Version drift", "How each agent version changed start-up context and cache lifetime"},
}

// Colors of the timeline marks.
const (
	rebuildColor    = 214
	compactionColor = 250
	subagentColor   = 141
	outputColor     = 110
)

var markColors = map[rune]int{markRebuild: rebuildColor, markCompaction: compactionColor, markSubagent: subagentColor, markOutput: outputColor}

// listLen is the number of rows that the up and down keys move through.
func (m model) listLen() int {
	switch m.screen {
	case advMenu:
		return len(menu)
	case advSpikes:
		return dayCount(m.f.from, m.f.to)
	case advSessions:
		used, _ := m.used()
		return len(sessions(perCall(used)))
	}
	return 0
}

func (m *model) open() {
	switch m.screen {
	case dashboard:
		m.screen, m.sel = advMenu, 0
	case advMenu:
		m.screen, m.sel = menu[m.sel].screen, 0
		if m.screen == advSpikes {
			used, _ := m.used()
			days := dayTotals(perCall(used), m.f.from, m.f.to)
			for i, d := range days {
				if d.tokens > days[m.sel].tokens {
					m.sel = i
				}
			}
		}
	case advSessions:
		used, _ := m.used()
		if list := sessions(perCall(used)); m.sel < len(list) {
			m.session, m.screen = list[m.sel].key, advTimeline
		}
	}
}

func (m *model) back() {
	switch m.screen {
	case advMenu:
		m.screen, m.sel = dashboard, 0
	case advTimeline:
		// The filters can change on the timeline, so find the session again.
		m.screen, m.sel = advSessions, 0
		used, _ := m.used()
		for i, s := range sessions(used) {
			if s.key == m.session {
				m.sel = i
			}
		}
	case advLimits, advSpikes, advRent, advSessions, advDrift:
		for i, item := range menu {
			if item.screen == m.screen {
				m.sel = i
			}
		}
		m.screen = advMenu
	}
}

// window returns the first row to show so that the selected row is visible.
func window(sel, rows int) int { return max(0, sel-rows+1) }

// marker draws the selection mark before a row.
func marker(selected bool, row string) string {
	if selected {
		return color(claudeColor, "› ") + bold(row)
	}
	return "  " + row
}

// perCall drops Copilot usage: Copilot logs one total per session, not the
// calls that the spike, session, timeline, and drift views look at.
func perCall(used []Usage) []Usage {
	var out []Usage
	for _, u := range used {
		if u.Tool != Copilot {
			out = append(out, u)
		}
	}
	return out
}

func (m model) advancedView(used []Usage, outputs []Output, w, rows int) []string {
	switch m.screen {
	case advSpikes, advSessions, advTimeline, advDrift:
		used = perCall(used)
	}
	switch m.screen {
	case advMenu:
		return m.menuView(w)
	case advLimits:
		return m.limitsView(used, w, rows)
	case advSpikes:
		return m.spikesView(used, outputs, w, rows)
	case advRent:
		return rentView(outputs, w, rows)
	case advSessions:
		return m.sessionsView(used, w, rows)
	case advTimeline:
		return m.timelineView(used, outputs, w, rows)
	case advDrift:
		return driftView(used, w, rows)
	}
	return nil
}

func (m model) menuView(w int) []string {
	const nameW = 18
	var lines []string
	for i, item := range menu {
		lines = append(lines, marker(i == m.sel, padRight(item.name, nameW))+color(dimColor, short(item.about, w-4-2-nameW)))
	}
	return panel("Advanced", color(dimColor, "enter opens a view"), w, lines)
}

func (m model) limitsView(used []Usage, w, rows int) []string {
	inner := w - 4
	var now []string
	// The limit is for the whole account, so this panel ignores the filters.
	if u, ok := latestLimit(m.usage); ok {
		l := u.Limit
		line := fmt.Sprintf("Plan %s · weekly %.0f%% used, resets %s", l.Plan, l.Week, l.WeekReset.Local().Format("Mon 2 Jan 15:04"))
		if !l.Hours5Reset.IsZero() {
			line += fmt.Sprintf(" · 5-hour %.0f%%, resets %s", l.Hours5, l.Hours5Reset.Local().Format("15:04"))
		}
		now = []string{short(line, inner), short(weekForecast(u, time.Now()), inner),
			color(dimColor, short("Points also include Codex use on other devices and on the web.", inner))}
	} else {
		now = []string{color(dimColor, "No Codex limit data in the logs.")}
	}
	lines := panel("Codex limits now", color(dimColor, "from the newest Codex call"), w, now)

	byProject, weeks := limitByProject(used), limitWeeks(used)
	rest := rows - len(lines) - 6
	weekRows := min(len(weeks), max(1, rest/2))
	projectRows := max(1, min(len(byProject), rest-weekRows))

	var total float64
	for _, r := range byProject {
		total += r.points
	}
	const numW = 9
	nameW := min(36, inner/3)
	barW := inner - nameW - 3*numW - 2
	who := []string{color(dimColor, padRight("Project", nameW)+padLeft("Points", numW)+padLeft("Share", numW)+"  "+padRight("", barW)+padLeft("Tokens", numW))}
	if len(byProject) == 0 {
		who = append(who, color(dimColor, "No Codex limit data for these filters."))
	}
	for _, r := range byProject[:min(projectRows, len(byProject))] {
		who = append(who, padRight(shortPath(r.name, nameW-1), nameW)+padLeft(fmt.Sprintf("%.0f", r.points), numW)+
			padLeft(fmt.Sprintf("%.0f%%", 100*r.points/total), numW)+"  "+
			color(codexColor, strings.Repeat("█", int(float64(barW)*r.points/byProject[0].points+0.5)))+
			strings.Repeat(" ", barW-int(float64(barW)*r.points/byProject[0].points+0.5))+padLeft(human(r.tokens), numW))
	}
	lines = append(lines, panel("Who used the weekly limit", color(dimColor, "percent points in this range"), w, who)...)

	var best float64
	for _, wk := range weeks {
		if wk.points > 0 {
			best = max(best, float64(wk.tokens)/wk.points)
		}
	}
	const dateW, planW = 12, 9
	barW = inner - dateW - planW - 3*numW - 2
	per := []string{color(dimColor, padRight("Week of", dateW)+padRight("Plan", planW)+padLeft("Points", numW)+padLeft("Tokens", numW)+
		padLeft("Per 1%", numW)+"  ")}
	if len(weeks) == 0 {
		per = append(per, color(dimColor, "No Codex limit data for these filters."))
	}
	for _, wk := range weeks[len(weeks)-min(weekRows, len(weeks)):] {
		rate, cells := "·", 0
		if wk.points > 0 {
			r := float64(wk.tokens) / wk.points
			rate, cells = human(int64(r)), int(float64(barW)*r/best+0.5)
		}
		per = append(per, padRight(wk.start.Format("Mon 2 Jan"), dateW)+padRight(wk.plan, planW)+padLeft(fmt.Sprintf("%.0f", wk.points), numW)+
			padLeft(human(wk.tokens), numW)+padLeft(bold(rate), numW)+"  "+color(codexColor, strings.Repeat("█", cells)))
	}
	return append(lines, panel("Tokens per 1% of the weekly limit", color(dimColor, "a jump means the limit changed"), w, per)...)
}

func (m model) spikesView(used []Usage, outputs []Output, w, rows int) []string {
	inner := w - 4
	days := dayTotals(used, m.f.from, m.f.to)
	totals := make([]int64, len(days))
	var peak int64
	for i, d := range days {
		totals[i] = d.tokens
		peak = max(peak, d.tokens)
	}
	med := median(totals)
	listRows := min(len(days), max(3, (rows-6)/2))
	const labelW, numW = 12, 9
	barW := inner - 2 - labelW - 2*numW - 2
	var list []string
	start := window(m.sel, listRows)
	for i := start; i < min(len(days), start+listRows); i++ {
		d := days[i]
		ratio := "·"
		if med > 0 && d.tokens > 0 {
			ratio = fmt.Sprintf("%.1f×", float64(d.tokens)/float64(med))
		}
		list = append(list, marker(i == m.sel, padRight(d.day.Format("Mon 02 Jan"), labelW)+padLeft(human(d.tokens), numW)+
			padLeft(ratio, numW)+"  "+bar(pair{d.tokens, 0}, peak, barW)))
	}
	lines := panel("Days", color(dimColor, "↑↓ picks a day · × is the share of the median day"), w, list)

	var why []string
	for _, kv := range dayText(explainDay(days[m.sel].day, days, used, outputs, m.titles)) {
		why = append(why, color(dimColor, padRight(kv[0], 11))+short(kv[1], inner-11))
	}
	why = why[:min(len(why), max(1, rows-len(lines)-2))]
	return append(lines, panel("Why "+days[m.sel].day.Format("Mon 2 Jan"), "", w, why)...)
}

// dayText writes the report of a day as label and text pairs.
func dayText(r dayReport) [][2]string {
	if r.total == 0 {
		return [][2]string{{"Total", "No tokens on this day."}}
	}
	line := human(r.total) + " tokens"
	if r.median > 0 {
		line += fmt.Sprintf(", %.1f× the median day (%s)", float64(r.total)/float64(r.median), human(r.median))
	}
	out := [][2]string{{"Total", line + "."}}
	for i, s := range r.sessions {
		label := ""
		if i == 0 {
			label = "Sessions"
		}
		out = append(out, [2]string{label, fmt.Sprintf("%3.0f%%  %s", 100*s.part, s.name)})
	}
	out = append(out, [2]string{"Context", fmt.Sprintf("calls sent a median of %s context tokens (range median %s).",
		human(r.context), human(r.rangeContext))})
	var cache []string
	if r.idle > 0 {
		cache = append(cache, fmt.Sprintf("%d rebuilds after more than 1 hour idle wrote %s tokens again", r.idle, human(r.idleTokens)))
	}
	if r.other > 0 && r.idle > 0 {
		cache = append(cache, fmt.Sprintf("%d other misses wrote %s", r.other, human(r.otherTokens)))
	} else if r.other > 0 {
		cache = append(cache, fmt.Sprintf("%d cache misses wrote %s tokens again", r.other, human(r.otherTokens)))
	}
	if len(cache) > 0 {
		out = append(out, [2]string{"Cache", strings.Join(cache, "; ") + "."})
	}
	if r.sub > 0 {
		out = append(out, [2]string{"Subagents", fmt.Sprintf("%.0f%% of the day's tokens.", 100*r.sub)})
	}
	if o := r.heaviest; o != nil {
		out = append(out, [2]string{"Outputs", fmt.Sprintf("%s %s added %s; %d later calls sent it again (%s).",
			o.Name, clip(o.Detail, 40), human(o.Size), o.Reads, human(o.Rent()))})
	}
	var parts []string
	for _, s := range r.models {
		parts = append(parts, fmt.Sprintf("%s %.0f%%", s.name, 100*s.part))
	}
	return append(out, [2]string{"Models", strings.Join(parts, ", ") + "."})
}

func rentView(outputs []Output, w, rows int) []string {
	inner := w - 4
	byTool := map[string]pair{}
	var total pair
	for _, o := range outputs {
		p := byTool[o.Name]
		p[o.Tool] += o.Rent()
		byTool[o.Name] = p
		total[o.Tool] += o.Rent()
	}
	tools := ranked(byTool)
	toolRows := max(1, min(len(tools), 5))
	lines := table("Context rent by tool", "Tool", tools, toolRows, total.sum(), []Tool{Claude, Codex}, w)

	sorted := append([]Output(nil), outputs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Rent() > sorted[j].Rent() })
	const numW, toolW = 9, 13
	projectW := min(24, inner/5)
	detailW := inner - 3*numW - toolW - projectW - 2
	top := []string{color(dimColor, padLeft("Rent", numW)+padLeft("Added", numW)+padLeft("Calls", numW)+"  "+
		padRight("Tool", toolW)+padRight("Command or file", detailW)+padRight("Project", projectW))}
	if len(sorted) == 0 {
		top = append(top, color(dimColor, "No tool outputs for these filters."))
	}
	n := max(1, rows-len(lines)-3)
	for _, o := range sorted[:min(n, len(sorted))] {
		c := claudeColor
		if o.Tool == Codex {
			c = codexColor
		}
		top = append(top, padLeft(bold(human(o.Rent())), numW)+padLeft(human(o.Size), numW)+padLeft(fmt.Sprint(o.Reads), numW)+"  "+
			color(c, padRight(short(o.Name, toolW-1), toolW))+padRight(clip(o.Detail, detailW-1), detailW)+
			color(dimColor, padRight(shortPath(o.Project, projectW), projectW)))
	}
	return append(lines, panel("Costliest outputs", color(dimColor, "rent = tokens added × later calls"), w, top)...)
}

func (m model) sessionsView(used []Usage, w, rows int) []string {
	inner := w - 4
	list := sessions(used)
	const dateW, numW = 14, 9
	nameW := inner - 2 - dateW - 3*numW
	lines := []string{color(dimColor, "  "+padRight("Started", dateW)+padRight("Session", nameW)+padLeft("Calls", numW)+
		padLeft("Peak ctx", numW)+padLeft("Tokens", numW))}
	if len(list) == 0 {
		lines = append(lines, color(dimColor, "No sessions for these filters."))
	}
	listRows := max(1, rows-3)
	start := window(m.sel, listRows)
	for i := start; i < min(len(list), start+listRows); i++ {
		s := list[i]
		name := sessionName(m.titles, s.key, s.project)
		c := claudeColor
		if s.tool == Codex {
			c = codexColor
		}
		row := padRight(s.start.Format("Mon 02 Jan"), dateW) + color(c, padRight(short(name, nameW-1), nameW)) +
			padLeft(fmt.Sprint(s.calls), numW) + padLeft(human(s.peak), numW) + padLeft(human(s.tokens), numW)
		lines = append(lines, marker(i == m.sel, row))
	}
	return panel("Sessions", color(dimColor, fmt.Sprintf("%d sessions · enter opens the timeline", len(list))), w, lines)
}

func (m model) timelineView(used []Usage, outputs []Output, w, rows int) []string {
	inner := w - 4
	calls, events := timeline(m.session, used, outputs)
	if len(calls) == 0 {
		return panel("Session", "", w, []string{color(dimColor, "This session has no calls for these filters.")})
	}
	name := sessionName(m.titles, m.session, calls[0].Project)
	var tokens, peak int64
	for _, u := range calls {
		tokens += u.Total()
		peak = max(peak, u.Context)
	}
	eventRows := min(len(events), max(3, rows/3))
	chartH := max(3, rows-6-max(eventRows, 1))

	const axisW = 8
	plotW := inner - axisW
	cols := min(len(calls), plotW)
	values := make([]int64, cols)
	marks := make([]rune, cols)
	col := func(call int) int { return call * cols / len(calls) }
	for i, u := range calls {
		values[col(i)] = max(values[col(i)], u.Context)
	}
	for _, e := range events {
		if c := col(e.call); markRank[e.mark] > markRank[marks[c]] {
			marks[c] = e.mark
		}
	}
	c := claudeColor
	if calls[0].Tool == Codex {
		c = codexColor
	}
	var chart []string
	for r := range chartH {
		label := ""
		switch r {
		case 0:
			label = human(peak)
		case chartH - 1:
			label = "0"
		}
		var sb strings.Builder
		level := (chartH - 1 - r) * 8
		for _, v := range values {
			v8 := int(float64(v) / float64(peak) * float64(chartH*8))
			switch {
			case v8 >= level+8:
				sb.WriteString("█")
			case v8 > level:
				sb.WriteString(string([]rune("▁▂▃▄▅▆▇")[v8-level-1]))
			default:
				sb.WriteString(" ")
			}
		}
		chart = append(chart, color(dimColor, padLeft(label, axisW-1))+" "+color(c, sb.String()))
	}
	var mrow strings.Builder
	for _, mk := range marks {
		if mk == 0 {
			mrow.WriteString(" ")
		} else {
			mrow.WriteString(color(markColors[mk], string(mk)))
		}
	}
	chart = append(chart, strings.Repeat(" ", axisW)+mrow.String())
	first, last := calls[0].Time, calls[len(calls)-1].Time
	layout := "15:04"
	if !day(first).Equal(day(last)) {
		layout = "2 Jan 15:04"
	}
	end := last.Format(layout)
	chart = append(chart, strings.Repeat(" ", axisW)+color(dimColor, padRight(first.Format(layout), cols-len(end))+end))

	legend := color(rebuildColor, "◆ rebuild") + "  " + color(compactionColor, "▼ compaction") + "  " +
		color(subagentColor, "◇ subagent") + "  " + color(outputColor, "● output")
	title := short(name, max(10, inner-width(legend)-6))
	lines := panel(title, legend, w, chart)

	// Keep the most important events, then show them in time order.
	shown := append([]event(nil), events...)
	sort.SliceStable(shown, func(i, j int) bool { return markRank[shown[i].mark] > markRank[shown[j].mark] })
	shown = shown[:min(eventRows, len(shown))]
	sort.SliceStable(shown, func(i, j int) bool { return shown[i].call < shown[j].call })
	var ev []string
	if len(events) == 0 {
		ev = append(ev, color(dimColor, "No cache rebuilds, compactions, subagents, or large outputs."))
	}
	for _, e := range shown {
		at := calls[e.call].Time.Format(layout)
		ev = append(ev, color(dimColor, at)+"  "+color(markColors[e.mark], string(e.mark))+" "+short(eventText(e, calls), inner-len(at)-4))
	}
	right := fmt.Sprintf("%d calls · %s tokens", len(calls), human(tokens))
	if len(events) > len(shown) {
		right = fmt.Sprintf("%d of %d events · %s", len(shown), len(events), right)
	}
	return append(lines, panel("Events", color(dimColor, right), w, ev)...)
}

// eventText says what happened at an event of the timeline.
func eventText(e event, calls []Usage) string {
	u, o := calls[e.call], e.out
	switch e.mark {
	case markRebuild:
		if u.Gap >= 5*time.Minute {
			return fmt.Sprintf("cache rebuilt after %s without calls: %s tokens written again", duration(u.Gap), human(u.Miss))
		}
		return fmt.Sprintf("cache missed: %s tokens written again", human(u.Miss))
	case markCompaction:
		if e.call == 0 {
			return "compaction: context down to " + human(u.Context)
		}
		return fmt.Sprintf("compaction: context %s → %s", human(calls[e.call-1].Context), human(u.Context))
	case markSubagent:
		return fmt.Sprintf("subagent result: %s added %s", clip(o.Detail, 50), human(o.Size))
	}
	return fmt.Sprintf("%s %s added %s; %d later calls sent it again", o.Name, clip(o.Detail, 50), human(o.Size), o.Reads)
}

func driftView(used []Usage, w, height int) []string {
	inner := w - 4
	rows := drift(used)
	// Keep the newest versions of each agent that fit: the panel uses two
	// border lines, the header, and three lines of notes.
	if fit := max(2, height-6); len(rows) > fit {
		var kept []driftRow
		for _, t := range []Tool{Claude, Codex} {
			var mine []driftRow
			for _, r := range rows {
				if r.tool == t {
					mine = append(mine, r)
				}
			}
			kept = append(kept, mine[len(mine)-min(len(mine), fit/2):]...)
		}
		rows = kept
	}
	const agentW, versionW, dateW, numW = 8, 9, 8, 9
	lines := []string{color(dimColor, padRight("Agent", agentW)+padRight("Version", versionW)+padRight("Since", dateW)+
		padLeft("Sessions", numW)+padLeft("First call", numW+2)+padLeft("Change", numW)+padLeft("1h cache", numW)+padLeft("1h (sub)", numW))}
	if len(rows) == 0 {
		lines = append(lines, color(dimColor, "No version data for these filters."))
	}
	pct := func(f float64) string {
		if f < 0 {
			return color(dimColor, "·")
		}
		return fmt.Sprintf("%.0f%%", 100*f)
	}
	for _, r := range rows {
		agent, c := "Claude", claudeColor
		if r.tool == Codex {
			agent, c = "Codex", codexColor
		}
		change := color(dimColor, "·")
		if !math.IsNaN(r.change) {
			change = fmt.Sprintf("%+.0f%%", 100*r.change)
			if r.change > 0.05 {
				change = color(errColor, change)
			}
		}
		sub := pct(r.sub1h)
		if r.tool == Codex {
			sub = color(dimColor, "·")
		}
		main := pct(r.share1h)
		if r.tool == Codex {
			main = color(dimColor, "·")
		}
		lines = append(lines, color(c, padRight(agent, agentW))+padRight(r.version, versionW)+padRight(r.first.Format("2 Jan"), dateW)+
			padLeft(fmt.Sprint(r.sessions), numW)+padLeft(bold(human(r.start)), numW+2)+padLeft(change, numW)+padLeft(main, numW)+padLeft(sub, numW))
	}
	lines = append(lines, "",
		color(dimColor, short("First call: context sent before the first reply (system prompt, tools, memory, skills).", inner)),
		color(dimColor, short("1h cache: share of cache writes kept for 1 hour. The rest expire after 5 minutes.", inner)))
	return panel("Version drift", color(dimColor, "a red change means more start-up context"), w, lines)
}

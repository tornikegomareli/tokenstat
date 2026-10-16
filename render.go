package main

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// 256-color palette codes.
const (
	claudeColor  = 173 // #d7875f
	codexColor   = 75  // #5fafff
	copilotColor = 114 // #87d787
	dimColor     = 244
	borderColor  = 239
	errColor     = 167 // #d75f5f
)

var useColor = os.Getenv("NO_COLOR") == ""

func color(c int, s string) string {
	if !useColor || s == "" {
		return s
	}
	return fmt.Sprintf("\x1b[38;5;%dm%s\x1b[0m", c, s)
}

func bold(s string) string {
	if !useColor {
		return s
	}
	return "\x1b[1m" + s + "\x1b[0m"
}

var ansiCode = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func width(s string) int { return utf8.RuneCountInString(ansiCode.ReplaceAllString(s, "")) }

func padRight(s string, w int) string { return s + strings.Repeat(" ", max(0, w-width(s))) }
func padLeft(s string, w int) string  { return strings.Repeat(" ", max(0, w-width(s))) + s }

// shortPath drops leading folders until the path fits in w cells. It cuts
// the last folder name only if that is not enough.
func shortPath(p string, w int) string {
	w = max(w, 1)
	parts := strings.Split(p, "/")
	for i := 1; utf8.RuneCountInString(p) > w && i < len(parts); i++ {
		p = "…/" + strings.Join(parts[i:], "/")
	}
	if r := []rune(p); len(r) > w {
		p = "…" + string(r[len(r)-w+1:])
	}
	return p
}

// clip cuts a command or a path to w cells. A path keeps its end, where the
// file name is.
func clip(s string, w int) string {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/") {
		return shortPath(s, w)
	}
	return short(s, w)
}

func human(n int64) string {
	f := float64(n)
	switch {
	case n == 0:
		return color(dimColor, "·")
	case n >= 1e12:
		return fmt.Sprintf("%.2fT", f/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.2fB", f/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", f/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1fK", f/1e3)
	}
	return fmt.Sprint(n)
}

// overflows counts the rows that panel had to cut. Tests expect none.
var overflows int

func panel(title, right string, w int, lines []string) []string {
	border := func(s string) string { return color(borderColor, s) }
	tail := ""
	if right != "" {
		tail = " " + right + " " + border("─")
	}
	fill := w - 5 - width(title) - width(tail)
	out := []string{border("╭─ ") + bold(title) + " " + border(strings.Repeat("─", fill)) + tail + border("╮")}
	for _, l := range lines {
		if width(l) > w-4 { // never break the frame, even if a row is too long
			l = short(ansiCode.ReplaceAllString(l, ""), w-4)
			overflows++
		}
		out = append(out, border("│")+" "+padRight(l, w-4)+" "+border("│"))
	}
	return append(out, border("╰"+strings.Repeat("─", w-2)+"╯"))
}

// The tools, indexed by Tool.
var (
	toolNames  = [numTools]string{"Claude Code", "Codex", "Copilot"}
	toolShort  = [numTools]string{"Claude", "Codex", "Copilot"}
	toolColors = [numTools]int{claudeColor, codexColor, copilotColor}
	toolChars  = [numTools]string{"█", "▒", "░"} // without color
)

// toolsIn returns the tools that have usage, in Tool order. Claude Code
// and Codex are always there, so the layout stays the same without Copilot.
func toolsIn(usage []Usage) []Tool {
	var seen [numTools]bool
	seen[Claude], seen[Codex] = true, true
	for _, u := range usage {
		seen[u.Tool] = true
	}
	var tools []Tool
	for t := range numTools {
		if seen[t] {
			tools = append(tools, t)
		}
	}
	return tools
}

// pair holds a token count per tool, indexed by Tool.
type pair [numTools]int64

func (p pair) sum() int64 {
	var s int64
	for _, v := range p {
		s += v
	}
	return s
}

func (p *pair) add(u Usage) { p[u.Tool] += u.Total() }

// bar draws the tokens of each tool as one stacked bar, scaled so that
// scale fills w cells.
func bar(p pair, scale int64, w int) string {
	if scale <= 0 || p.sum() == 0 {
		return strings.Repeat(" ", w)
	}
	var b strings.Builder
	var cum int64
	drawn := 0
	for t := range numTools {
		cum += p[t]
		end := int(float64(cum)/float64(scale)*float64(w) + 0.5)
		if end > drawn {
			char := "█"
			if !useColor {
				char = toolChars[t]
			}
			b.WriteString(color(toolColors[t], strings.Repeat(char, end-drawn)))
			drawn = end
		}
	}
	if drawn == 0 {
		big := Claude
		for t := range numTools {
			if p[t] > p[big] {
				big = t
			}
		}
		return color(toolColors[big], "▏") + strings.Repeat(" ", w-1)
	}
	return b.String() + strings.Repeat(" ", w-drawn)
}

// chart draws one stacked bar per row with the tokens of each tool and the
// total on the right.
func chart(labels []string, values []pair, highlight int, tools []Tool, inner int) []string {
	const labelW, numW = 10, 8
	barW := inner - labelW - 3 - (len(tools)+1)*numW
	var scale int64
	for _, v := range values {
		scale = max(scale, v.sum())
	}
	head := padRight("", labelW+2+barW+1)
	for _, t := range tools {
		head += padLeft(color(toolColors[t], toolShort[t]), numW)
	}
	lines := []string{head + padLeft("Total", numW)}
	for i, v := range values {
		label := padRight(labels[i], labelW)
		if i == highlight {
			label = bold(label)
		} else if v.sum() == 0 {
			label = color(dimColor, label)
		}
		row := label + "  " + bar(v, scale, barW) + " "
		for _, t := range tools {
			row += padLeft(human(v[t]), numW)
		}
		lines = append(lines, row+padLeft(bold(human(v.sum())), numW))
	}
	return lines
}

const minWidth, minHeight, maxWidth = 80, 24, 140

func (m model) View() string {
	if !m.loaded || m.width == 0 {
		return "\n  Reading Claude Code, Codex, and Copilot logs…"
	}
	if m.width < minWidth || m.height < minHeight {
		return fmt.Sprintf("\n  tokenstat needs a window of at least %d×%d. This one is %d×%d.",
			minWidth, minHeight, m.width, m.height)
	}
	w := min(m.width, maxWidth)
	used, outputs := m.used()
	lines := m.summary(used, w)
	rows := m.height - len(lines) - 1
	switch {
	case m.mode == pickingProject || m.mode == pickingModel:
		lines = append(lines, m.picker(w, rows)...)
	case m.screen == dashboard:
		lines = append(lines, m.panels(used, w, rows)...)
	default:
		lines = append(lines, m.advancedView(used, outputs, w, rows)...)
	}
	lines = lines[:min(len(lines), m.height-1)]
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, m.footer()), "\n")
}

func dayCount(from, to time.Time) int {
	return int(math.Round(to.AddDate(0, 0, 1).Sub(from).Hours() / 24))
}

func (m model) rangeLabel() string {
	from, to := m.f.from, m.f.to
	n := dayCount(from, to)
	if n == 1 {
		return from.Format("Mon 2 Jan 2006")
	}
	start := from.Format("2 Jan")
	if from.Year() != to.Year() {
		start = from.Format("2 Jan 2006")
	}
	return fmt.Sprintf("%s – %s · %d days", start, to.Format("2 Jan 2006"), n)
}

func (m model) filterLine(inner int) string {
	tool := "All"
	if t := m.f.tool; t >= 0 {
		tool = color(toolColors[t], toolNames[t])
	}
	model := "All"
	if m.f.model != "" {
		model = bold(m.f.model)
	}
	head := color(dimColor, "Tool ") + tool + color(dimColor, "   Model ") + model + color(dimColor, "   Project ")
	note := ""
	if m.failed > 0 {
		note = color(errColor, fmt.Sprintf("   %d files could not be read", m.failed))
	}
	// The leaderboard account goes last, in the space the filters leave:
	// in full, shorter, or not at all. The project keeps room for 12 cells.
	project, projectW := "All", 3
	if m.f.project != "" {
		projectW = min(width(m.f.project), 12)
	}
	account := []string{
		color(dimColor, "   Leaderboard ") + "github.com/" + m.login,
		color(dimColor, "   ") + "github.com/" + m.login,
		"",
	}
	if m.login == "" {
		account = []string{color(dimColor, "   Leaderboard not logged in"), color(dimColor, "   not logged in"), ""}
	}
	for _, a := range account {
		if width(head)+projectW+width(note)+width(a) <= inner || a == "" {
			note += a
			break
		}
	}
	if m.f.project != "" {
		project = bold(shortPath(m.f.project, inner-width(head)-width(note)))
	}
	return head + project + note
}

func (m model) summary(used []Usage, w int) []string {
	var mix [numTools + 1]Usage // by Tool, then all tools
	for _, u := range used {
		for _, x := range []*Usage{&mix[u.Tool], &mix[numTools]} {
			x.Input += u.Input
			x.Output += u.Output
			x.CacheRead += u.CacheRead
			x.CacheWrite += u.CacheWrite
		}
	}
	const labelW, colW = 12, 12
	lines := []string{m.filterLine(w - 4), color(dimColor, padRight("", labelW)+padLeft("Input", colW)+
		padLeft("Output", colW)+padLeft("Cache read", colW)+padLeft("Cache write", colW)+padLeft("Total", colW))}
	rows := []int{}
	for _, t := range toolsIn(m.usage) {
		rows = append(rows, int(t))
	}
	for _, i := range append(rows, int(numTools)) {
		label, u := bold("Total"), mix[i]
		if i < int(numTools) {
			label = color(toolColors[i], toolNames[i])
		}
		lines = append(lines, padRight(label, labelW)+padLeft(human(u.Input), colW)+padLeft(human(u.Output), colW)+
			padLeft(human(u.CacheRead), colW)+padLeft(human(u.CacheWrite), colW)+padLeft(bold(human(u.Total())), colW))
	}
	return panel("tokenstat", color(dimColor, m.rangeLabel()), w, lines)
}

// panels fills rows lines with the chart, the projects, and the models.
func (m model) panels(used []Usage, w, rows int) []string {
	projects, models := map[string]pair{}, map[string]pair{}
	var total pair
	for _, u := range used {
		p := projects[u.Project]
		p.add(u)
		projects[u.Project] = p
		q := models[u.Model]
		q.add(u)
		models[u.Model] = q
		total.add(u)
	}

	// Each panel uses two border lines and one header line.
	avail := rows - 9
	clamp := func(n, lo, hi int) int { return max(lo, min(n, hi)) }
	modelRows := clamp(len(models), 1, min(5, avail/5))
	projectMin := clamp(len(projects), 1, avail/5)
	title, bs := buckets(m.f.from, m.f.to, max(1, avail-modelRows-projectMin))
	projectRows := clamp(len(projects), 1, avail-modelRows-len(bs))

	labels := make([]string, len(bs))
	values := make([]pair, len(bs))
	today, highlight := day(time.Now()), -1
	for i, b := range bs {
		labels[i] = b.label
		if !today.Before(b.from) && today.Before(b.to) {
			highlight = i
		}
	}
	for _, u := range used {
		if i := sort.Search(len(bs), func(i int) bool { return u.Time.Before(bs[i].to) }); i < len(bs) {
			values[i].add(u)
		}
	}

	tools := toolsIn(m.usage)
	lines := panel(title, "", w, chart(labels, values, highlight, tools, w-4))
	lines = append(lines, table("Projects", "Project", ranked(projects), projectRows, total.sum(), tools, w)...)
	return append(lines, table("Models", "Model", ranked(models), modelRows, total.sum(), tools, w)...)
}

// table lists the items with the most tokens and their share of all tokens.
func table(title, column string, items []item, rows int, total int64, tools []Tool, w int) []string {
	inner := w - 4
	const colW, pctW = 9, 5
	cols := len(tools) + 1
	nameW := min(36, (inner-cols*colW-pctW-2)/2)
	barW := inner - nameW - cols*colW - pctW - 2
	head := color(dimColor, padRight(column, nameW))
	for _, t := range tools {
		head += padLeft(color(toolColors[t], toolShort[t]), colW)
	}
	lines := []string{head + color(dimColor, padLeft("Total", colW)+"  "+padRight("", barW)+padLeft("share", pctW))}
	if len(items) == 0 {
		lines = append(lines, color(dimColor, "No usage for these filters"))
	}
	for _, it := range items[:min(rows, len(items))] {
		pct := fmt.Sprintf("%.0f%%", 100*float64(it.tokens.sum())/float64(total))
		row := padRight(shortPath(it.name, nameW-1), nameW)
		for _, t := range tools {
			row += padLeft(human(it.tokens[t]), colW)
		}
		lines = append(lines, row+padLeft(bold(human(it.tokens.sum())), colW)+"  "+
			bar(it.tokens, items[0].tokens.sum(), barW)+padLeft(pct, pctW))
	}
	right := ""
	if len(items) > rows {
		right = color(dimColor, fmt.Sprintf("top %d of %d", rows, len(items)))
	}
	return panel(title, right, w, lines)
}

func (m model) picker(w, rows int) []string {
	items := m.pickItems()
	what, all := "project", "All projects"
	if m.mode == pickingModel {
		what, all = "model", "All models"
	}
	inner := w - 4
	listRows := max(1, rows-3)
	start := max(0, m.cursor-listRows+1)
	lines := []string{color(dimColor, "Search ") + m.input + "▏"}
	for i := start; i < min(len(items), start+listRows); i++ {
		name := items[i].name
		if name == "" {
			name = all
		}
		row := padRight(shortPath(name, inner-14), inner-12) + padLeft(human(items[i].tokens.sum()), 10)
		if i == m.cursor {
			row = color(claudeColor, "› ") + bold(row)
		} else {
			row = "  " + row
		}
		lines = append(lines, row)
	}
	found := len(items)
	if m.input == "" {
		found-- // the "All" item
	}
	return panel("Pick a "+what, color(dimColor, fmt.Sprintf("%d found", found)), w, lines)
}

func (m model) footer() string {
	if m.mode == typingRange {
		hint := color(dimColor, "   YYYY-MM-DD [YYYY-MM-DD] · enter apply · esc cancel")
		if m.inputErr != "" {
			hint = "   " + color(errColor, m.inputErr)
		}
		return " " + bold("Range ") + m.input + "▏" + hint
	}
	if m.status != "" && m.mode == browsing {
		c := dimColor
		if m.statusErr {
			c = errColor
		}
		return " " + color(c, short(m.status, max(m.width-2, 10)))
	}
	keys := [][2]string{{"d", "range"}, {"c", "custom"}, {"t", "tool"}, {"p", "project"}, {"m", "model"}, {"a", "advanced"}, {"s", "submit to leaderboard"}, {"r", "reset"}, {"q", "quit"}}
	switch {
	case m.mode != browsing:
		keys = [][2]string{{"type", "to search"}, {"↑↓", "move"}, {"enter", "select"}, {"esc", "cancel"}}
	case m.screen != dashboard:
		keys = [][2]string{{"esc", "back"}, {"↑↓", "move"}, {"enter", "open"}, {"d c t p m", "filters"}, {"r", "reset"}, {"q", "quit"}}
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, bold(k[0])+" "+color(dimColor, k[1]))
	}
	line := " " + strings.Join(parts, "  ")
	// Too wide: shorten the s key, which is the newest, then drop it.
	for _, label := range []string{"submit", ""} {
		if m.width == 0 || width(line) <= m.width {
			break
		}
		i := slices.IndexFunc(parts, func(p string) bool { return strings.HasPrefix(p, bold("s")+" ") })
		if i < 0 {
			break
		}
		if label == "" {
			parts = slices.Delete(parts, i, i+1)
		} else {
			parts[i] = bold("s") + " " + color(dimColor, label)
		}
		line = " " + strings.Join(parts, "  ")
	}
	return line
}

package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const dateFmt = "2006-01-02"

// presets are the date ranges that the d key cycles through, in days.
// 1 is today only.
var presets = []int{1, 7, 14, 30, 90}

const defaultPreset = 2 // 14 days

type mode int

const (
	browsing mode = iota
	pickingProject
	pickingModel
	typingRange
)

// filters select the usage that the panels show.
type filters struct {
	from, to time.Time // first and last day, at local midnight
	tool     int       // -1 for all tools, else a Tool
	project  string    // "" for all projects
	model    string    // "" for all models
}

func (f filters) keep(u Usage) bool { return f.keepAt(u.Time, u.Tool, u.Project, u.Model) }

func (f filters) keepAt(t time.Time, tool Tool, project, model string) bool {
	return !t.Before(f.from) && t.Before(f.to.AddDate(0, 0, 1)) &&
		(f.tool < 0 || tool == Tool(f.tool)) &&
		(f.project == "" || project == f.project) &&
		(f.model == "" || model == f.model)
}

type model struct {
	data
	loaded        bool
	width, height int
	preset        int // index into presets, or -1 for a custom range
	f             filters
	mode          mode
	input         string // search text, or the custom range being typed
	inputErr      string
	cursor        int
	screen        screen
	sel           int    // selected row on an Advanced screen
	session       string // session that the timeline shows
	submitting    bool   // the s key is logging in or sending, and waits for the reply
	status        string // what the s key is doing or did, shown in the footer until the next key
	statusErr     bool
	login         string // GitHub login of the leaderboard account, "" when logged out
	loginTried    bool   // this s press already ran a login, so do not loop
	cancelLogin   func() // stops the wait for GitHub; nil when not waiting
	loginGen      int    // counts cancels, so a cancelled login's reply is dropped
}

// The s key logs in first when there is no login, then submits. Each step
// runs as a command and replies with one of these.
type (
	loginStartedMsg struct {
		code deviceCode
		err  error
	}
	loggedInMsg struct {
		gen   int
		login string
		err   error
	}
	submittedMsg struct {
		text string
		err  error
	}
)

func submitCmd(usage []Usage) tea.Cmd {
	return func() tea.Msg {
		text, err := submit(usage, defaultSubmitDays, "")
		return submittedMsg{text, err}
	}
}

func startLoginCmd() tea.Msg {
	code, err := startLogin("")
	return loginStartedMsg{code, err}
}

// fail ends the s key's work with an error in the footer.
func (m *model) fail(err error) {
	m.submitting, m.status, m.statusErr = false, err.Error(), true
}

// used returns the usage and the tool outputs that the filters keep.
func (m model) used() ([]Usage, []Output) {
	var usage []Usage
	for _, u := range m.usage {
		if m.f.keep(u) {
			usage = append(usage, u)
		}
	}
	var outputs []Output
	for _, o := range m.outputs {
		if m.f.keepAt(o.Time, o.Tool, o.Project, o.Model) {
			outputs = append(outputs, o)
		}
	}
	return usage, outputs
}

type loadedMsg data

func newModel() model {
	m := model{f: filters{tool: -1}}
	m.setPreset(defaultPreset, time.Now())
	return m
}

func day(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

func (m *model) setPreset(i int, now time.Time) {
	m.preset = i
	m.f.to = day(now)
	m.f.from = m.f.to.AddDate(0, 0, 1-presets[i])
}

func (m model) Init() tea.Cmd {
	return func() tea.Msg { return loadedMsg(load()) }
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case loadedMsg:
		m.data, m.loaded = data(msg), true
		if c, err := readCredentials(); err == nil {
			m.login = c.Login
		}
	case loginStartedMsg:
		if msg.err != nil {
			m.fail(msg.err)
			break
		}
		m.status = fmt.Sprintf("Enter %s at %s · esc to cancel", msg.code.UserCode, msg.code.VerificationURI)
		ctx, cancel := context.WithCancel(context.Background())
		code, gen := msg.code, m.loginGen
		m.cancelLogin = cancel
		return m, func() tea.Msg {
			login, err := finishLogin(ctx, code)
			return loggedInMsg{gen, login, err}
		}
	case loggedInMsg:
		if msg.gen != m.loginGen {
			break // cancelled with esc
		}
		m.cancelLogin = nil
		if msg.err != nil {
			m.fail(msg.err)
			break
		}
		m.login, m.status = msg.login, "Logged in as "+msg.login+". Sending your daily totals to the leaderboard…"
		return m, submitCmd(m.usage)
	case submittedMsg:
		if errors.Is(msg.err, errNotLoggedIn) && !m.loginTried {
			// No login yet, or the server no longer takes it: log in, then
			// send again.
			m.loginTried, m.login = true, ""
			m.status = "Not logged in. Starting the GitHub login…"
			return m, startLoginCmd
		}
		if msg.err != nil {
			m.fail(msg.err)
			break
		}
		m.submitting, m.status, m.statusErr = false, msg.text, false
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		if msg.Type == tea.KeyEsc && m.cancelLogin != nil {
			m.cancelLogin()
			m.cancelLogin, m.loginGen = nil, m.loginGen+1
			m.submitting, m.status, m.statusErr = false, "Login cancelled.", false
			return m, nil
		}
		if !m.submitting {
			m.status = ""
		}
		var cmd tea.Cmd
		switch m.mode {
		case browsing:
			if msg.String() == "q" {
				return m, tea.Quit
			}
			cmd = m.browse(msg)
		case typingRange:
			m.typeRange(msg)
		default:
			m.pick(msg)
		}
		// A filter can shrink the list, so keep the selection on it.
		m.sel = max(0, min(m.sel, m.listLen()-1))
		return m, cmd
	}
	return m, nil
}

func (m *model) browse(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "s":
		if !m.loaded || m.submitting {
			return nil
		}
		m.submitting, m.loginTried = true, false
		m.status, m.statusErr = "Sending your daily totals to the leaderboard…", false
		return submitCmd(m.usage)
	case "d":
		m.setPreset((m.preset+1)%len(presets), time.Now())
	case "c":
		m.mode, m.inputErr = typingRange, ""
		m.input = m.f.from.Format(dateFmt) + " " + m.f.to.Format(dateFmt)
	case "t":
		next := -1 // after the last tool, all tools
		for _, t := range toolsIn(m.usage) {
			if int(t) > m.f.tool {
				next = int(t)
				break
			}
		}
		m.f.tool = next
	case "p":
		m.mode, m.input, m.cursor = pickingProject, "", 0
	case "m":
		m.mode, m.input, m.cursor = pickingModel, "", 0
	case "r":
		m.f = filters{tool: -1}
		m.setPreset(defaultPreset, time.Now())
	case "a":
		if m.screen == dashboard {
			m.open()
		}
	case "esc":
		m.back()
	case "enter":
		if m.screen != dashboard {
			m.open()
		}
	case "up":
		m.sel = max(0, m.sel-1)
	case "down":
		m.sel++
	}
	return nil
}

// edit applies a key to the text input. It reports whether the text changed.
func (m *model) edit(k tea.KeyMsg) bool {
	switch k.Type {
	case tea.KeyRunes:
		m.input += string(k.Runes)
	case tea.KeySpace:
		m.input += " "
	case tea.KeyBackspace:
		r := []rune(m.input)
		if len(r) == 0 {
			return false
		}
		m.input = string(r[:len(r)-1])
	default:
		return false
	}
	return true
}

func (m *model) typeRange(k tea.KeyMsg) {
	switch k.Type {
	case tea.KeyEsc:
		m.mode = browsing
	case tea.KeyEnter:
		from, to, err := parseRange(m.input, time.Now())
		if err != nil {
			m.inputErr = err.Error()
			return
		}
		m.f.from, m.f.to, m.preset, m.mode = from, to, -1, browsing
	default:
		if m.edit(k) {
			m.inputErr = ""
		}
	}
}

// parseRange reads "YYYY-MM-DD [YYYY-MM-DD]". Without a second date the
// range ends today.
func parseRange(s string, now time.Time) (from, to time.Time, err error) {
	parts := strings.Fields(s)
	if len(parts) == 0 || len(parts) > 2 {
		return from, to, fmt.Errorf("type one or two dates")
	}
	to = day(now)
	for i, p := range parts {
		d, err := time.ParseInLocation(dateFmt, p, time.Local)
		if err != nil {
			return from, to, fmt.Errorf("%q is not a YYYY-MM-DD date", p)
		}
		if i == 0 {
			from = d
		} else {
			to = d
		}
	}
	if from.After(to) {
		return from, to, fmt.Errorf("the first date is after the second")
	}
	return from, to, nil
}

func (m *model) pick(k tea.KeyMsg) {
	items := m.pickItems()
	switch k.Type {
	case tea.KeyEsc:
		m.mode = browsing
	case tea.KeyUp:
		m.cursor = max(0, m.cursor-1)
	case tea.KeyDown:
		m.cursor = max(0, min(len(items)-1, m.cursor+1))
	case tea.KeyEnter:
		if len(items) == 0 {
			return
		}
		if m.mode == pickingProject {
			m.f.project = items[m.cursor].name
		} else {
			m.f.model = items[m.cursor].name
		}
		m.mode = browsing
	default:
		if m.edit(k) {
			m.cursor = 0
		}
	}
}

// item is a project or model with its tokens. The name "" means all.
type item struct {
	name   string
	tokens pair
}

// ranked sorts the totals by tokens, most first.
func ranked(totals map[string]pair) []item {
	items := make([]item, 0, len(totals))
	for name, p := range totals {
		items = append(items, item{name, p})
	}
	sort.Slice(items, func(i, j int) bool {
		if a, b := items[i].tokens.sum(), items[j].tokens.sum(); a != b {
			return a > b
		}
		return items[i].name < items[j].name
	})
	return items
}

// pickItems lists the projects or models that the other filters keep and
// whose name contains the search text. Without search text, the first item
// selects all of them.
func (m model) pickItems() []item {
	f := m.f
	if m.mode == pickingProject {
		f.project = ""
	} else {
		f.model = ""
	}
	totals := map[string]pair{}
	var all pair
	for _, u := range m.usage {
		if !f.keep(u) {
			continue
		}
		name := u.Project
		if m.mode == pickingModel {
			name = u.Model
		}
		p := totals[name]
		p.add(u)
		totals[name] = p
		all.add(u)
	}
	var items []item
	if m.input == "" {
		items = append(items, item{"", all})
	}
	search := strings.ToLower(m.input)
	for _, it := range ranked(totals) {
		if strings.Contains(strings.ToLower(it.name), search) {
			items = append(items, it)
		}
	}
	return items
}

type bucket struct {
	label    string
	from, to time.Time // to is exclusive
}

// buckets splits the range into days. If there are more than maxRows days
// it uses weeks, then months. Months can be more than maxRows.
func buckets(from, to time.Time, maxRows int) (string, []bucket) {
	end := to.AddDate(0, 0, 1)
	days := int(math.Round(end.Sub(from).Hours() / 24))
	var bs []bucket
	switch {
	case days <= maxRows:
		for d := from; d.Before(end); d = d.AddDate(0, 0, 1) {
			bs = append(bs, bucket{d.Format("Mon 02 Jan"), d, d.AddDate(0, 0, 1)})
		}
		return "Daily", bs
	case (days+6)/7 <= maxRows:
		for d := from; d.Before(end); d = d.AddDate(0, 0, 7) {
			bs = append(bs, bucket{d.Format("02 Jan"), d, earliest(d.AddDate(0, 0, 7), end)})
		}
		return "Weekly", bs
	}
	for d := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.Local); d.Before(end); d = d.AddDate(0, 1, 0) {
		start := from
		if d.After(from) {
			start = d
		}
		bs = append(bs, bucket{d.Format("Jan 2006"), start, earliest(d.AddDate(0, 1, 0), end)})
	}
	return "Monthly", bs
}

func earliest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

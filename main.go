// tokenstat shows Claude Code, Codex, and Copilot CLI token usage from the local session logs.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if ran, err := runCommand(os.Args[1:]); ran {
		if err != nil {
			fmt.Fprintln(os.Stderr, "tokenstat:", err)
			os.Exit(1)
		}
		return
	}
	if _, err := tea.NewProgram(newModel(), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tokenstat:", err)
		os.Exit(1)
	}
}

// data is everything tokenstat reads from the logs.
type data struct {
	usage   []Usage
	outputs []Output
	titles  map[string]string // session title by log file
	failed  int               // files that could not be read
}

// load reads every Claude Code, Codex, Copilot CLI, and VS Code Copilot Chat session log.
func load() data {
	home, _ := os.UserHomeDir()
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	claude, failedClaude := scan(findJSONL(filepath.Join(home, ".claude", "projects"), filepath.Join(home, ".config", "claude", "projects")), parseClaude)
	codex, failedCodex := scan(findJSONL(filepath.Join(codexHome, "sessions"), filepath.Join(codexHome, "archived_sessions")), parseCodex)
	copilot, failedCopilot := scan(findJSONL(filepath.Join(home, ".copilot", "session-state")), parseCopilot)
	vscode, failedVSCode := scan(findChatSessions(vscodeRoots()...), parseVSCode)

	var d data
	d.failed = failedClaude + failedCodex + failedCopilot + failedVSCode
	titleByID := codexTitles(filepath.Join(codexHome, "session_index.jsonl"))
	var recs [numTools][]record
	var outputs []Output
	for _, files := range []map[string]fileData{claude, codex, copilot, vscode} {
		for _, fd := range files {
			if fd.title != "" {
				titleByID[fd.id] = fd.title
			}
			for _, r := range fd.records {
				recs[r.Tool] = append(recs[r.Tool], r)
			}
			outputs = append(outputs, fd.outputs...)
		}
	}
	d.titles = map[string]string{}
	for _, files := range []map[string]fileData{claude, codex, copilot, vscode} {
		for path, fd := range files {
			title := titleByID[fd.id]
			if title == "" {
				title = titleByID[fd.parent]
			}
			if title != "" && fd.sub {
				title += " (subagent)"
			}
			d.titles[path] = title
		}
	}

	d.usage = append(append(dedupe(recs[Claude]), dedupe(recs[Codex])...), dedupe(recs[Copilot])...)
	// Projects use / on every system, so Windows paths shorten like the others.
	home = filepath.ToSlash(home)
	for i := range d.usage {
		d.usage[i].Time = d.usage[i].Time.Local()
		d.usage[i].Project = tilde(filepath.ToSlash(d.usage[i].Project), home)
	}
	d.outputs = dedupeOutputs(outputs)
	for i := range d.outputs {
		d.outputs[i].Time = d.outputs[i].Time.Local()
		d.outputs[i].Project = tilde(filepath.ToSlash(d.outputs[i].Project), home)
		d.outputs[i].Detail = strings.ReplaceAll(d.outputs[i].Detail, home, "~")
	}
	setLimitPoints(d.usage)
	return d
}

// tilde writes a path in the home folder as ~/...
func tilde(path, home string) string {
	if rest, ok := strings.CutPrefix(path, home); ok && home != "" && (rest == "" || rest[0] == '/') {
		return "~" + rest
	}
	return path
}

// codexTitles reads the thread names that Codex keeps for its sessions.
func codexTitles(path string) map[string]string {
	titles := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return titles
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var t struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal(sc.Bytes(), &t) == nil && t.Name != "" {
			titles[t.ID] = t.Name
		}
	}
	return titles
}

// setLimitPoints gives each Codex call the weekly limit percent that it
// used: the rise in the percent since the call before it in the same week.
// The limit covers the whole account, so calls of all sessions count in
// time order. The first call of a week only sets the start, because its
// percent includes use before it, maybe on another device.
func setLimitPoints(usage []Usage) {
	var idx []int
	for i, u := range usage {
		if u.Limit != nil {
			idx = append(idx, i)
		}
	}
	sort.Slice(idx, func(a, b int) bool { return usage[idx[a]].Time.Before(usage[idx[b]].Time) })
	last := map[int64]float64{} // percent by week
	for _, i := range idx {
		l := usage[i].Limit
		prev, seen := last[l.WeekResetBucket]
		if rise := l.Week - prev; seen && rise > 0 {
			usage[i].Points = rise
		}
		last[l.WeekResetBucket] = max(prev, l.Week)
	}
}

// findJSONL returns every .jsonl file under the roots that exist.
func findJSONL(roots ...string) []string {
	var files []string
	for _, root := range roots {
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
				files = append(files, path)
			}
			return nil
		})
	}
	return files
}

// scan parses the files in parallel. It returns what each file holds, by
// path, and the number of files it could not read.
func scan(files []string, parse func(string, io.Reader) (fileData, error)) (map[string]fileData, int) {
	var (
		mu     sync.Mutex
		out    = map[string]fileData{}
		failed int
		wg     sync.WaitGroup
	)
	paths := make(chan string)
	for range runtime.NumCPU() {
		wg.Go(func() {
			for path := range paths {
				fd, err := parseFile(path, parse)
				mu.Lock()
				out[path] = fd
				if err != nil {
					failed++
				}
				mu.Unlock()
			}
		})
	}
	for _, f := range files {
		paths <- f
	}
	close(paths)
	wg.Wait()
	return out, failed
}

func parseFile(path string, parse func(string, io.Reader) (fileData, error)) (fileData, error) {
	f, err := os.Open(path)
	if err != nil {
		return fileData{}, err
	}
	defer f.Close()
	return parse(path, f)
}

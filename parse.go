package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

type Tool int

const (
	Claude Tool = iota
	Codex
	Copilot
	numTools
)

// Tokens are the token counts of one model call.
type Tokens struct {
	Input      int64 // input tokens not read from cache
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

func (t Tokens) Total() int64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

// Usage is one model call.
type Usage struct {
	Tokens
	Time    time.Time
	Project string
	Tool    Tool
	Model   string
	Session string        // the log file, which holds one context window
	Version string        // agent version
	Sub     bool          // a subagent made the call
	First   bool          // first call of the session
	Compact bool          // the context shrank since the previous call: a compaction
	Context int64         // context tokens sent with the call
	Gap     time.Duration // time since the previous call of the session
	Miss    int64         // context tokens written again because the cache missed
	Cache1h int64         // cache writes with the 1-hour lifetime (Claude Code)
	Limit   *Limit        // Codex plan limits after the call
	Points  float64       // weekly limit percent this call used (set by load)
	Calls   int           // model calls this record stands for; 0 means one (Copilot sums several)
}

// Limit is a snapshot of the Codex plan limits.
type Limit struct {
	Plan            string
	Week, Hours5    float64 // percent used
	WeekReset       time.Time
	Hours5Reset     time.Time
	WeekResetBucket int64 // reset time in hours, which groups snapshots of one week
}

// Output is a tool result that stayed in the context. Every later call
// sends it again until a compaction removes it.
type Output struct {
	key     string
	Time    time.Time
	Project string
	Tool    Tool
	Model   string
	Session string
	Name    string // tool name, such as Bash or Read
	Detail  string // command, path, or query
	Size    int64  // context tokens added before the next call
	Reads   int    // later calls that sent it again
}

// Rent is the tokens that later calls spent to send the output again.
func (o Output) Rent() int64 { return o.Size * int64(o.Reads) }

// record is a Usage with a key that identifies the same call when it
// appears more than once (streamed chunks, resumed or forked sessions).
type record struct {
	key string
	Usage
}

// fileData is what one log file holds.
type fileData struct {
	records []record
	outputs []Output
	id      string // session id
	parent  string // parent session id of a Codex subagent
	title   string
	sub     bool // a subagent wrote the log
}

const (
	missMin    = 5000 // smaller cache misses are noise
	compaction = 0.6  // context below this share of the previous call means a compaction
)

// eachLine calls fn for every line in r. Transcript lines can be many MB long.
func eachLine(r io.Reader, fn func([]byte)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<30)
	for sc.Scan() {
		fn(sc.Bytes())
	}
	return sc.Err()
}

// call is a model call and the tool outputs that arrived before it.
type call struct {
	rec   record
	tools []toolUse
}

type toolUse struct{ name, detail string }

// finish sets the fields that depend on the calls around each call of one
// session, and returns the tool outputs with their size and reads.
func finish(calls []*call) ([]record, []Output) {
	for i := 1; i < len(calls); i++ {
		u := &calls[i].rec.Usage
		u.Compact = float64(u.Context) < compaction*float64(calls[i-1].rec.Context)
	}
	next := make([]int, len(calls)) // index of the first compaction after each call
	cut := len(calls)
	for i := len(calls) - 1; i >= 0; i-- {
		next[i] = cut
		if calls[i].rec.Compact {
			cut = i
		}
	}
	recs := make([]record, len(calls))
	var outs []Output
	for i, c := range calls {
		u := &c.rec.Usage
		u.First = i == 0
		if i > 0 {
			prev := calls[i-1].rec.Usage
			u.Gap = u.Time.Sub(prev.Time)
			growth := u.Context - prev.Context
			if miss := u.Input + u.CacheWrite - max(growth, 0); miss >= missMin && !u.Compact {
				u.Miss = miss
			}
			if growth > 0 && len(c.tools) > 0 {
				detail := c.tools[0].detail
				if len(c.tools) > 1 {
					detail += fmt.Sprintf(" (+%d more)", len(c.tools)-1)
				}
				outs = append(outs, Output{
					key: c.rec.key, Time: u.Time, Project: u.Project, Tool: u.Tool, Model: u.Model, Session: u.Session,
					Name: c.tools[0].name, Detail: detail, Size: growth, Reads: next[i] - i - 1,
				})
			}
		}
		recs[i] = c.rec
	}
	return recs, outs
}

// short returns the first line of s, cut to n runes.
func short(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}

type claudeLine struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Cwd       string    `json:"cwd"`
	SessionID string    `json:"sessionId"`
	Version   string    `json:"version"`
	RequestID string    `json:"requestId"`
	AITitle   string    `json:"aiTitle"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input      int64 `json:"input_tokens"`
			Output     int64 `json:"output_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
			Cache      struct {
				OneHour int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

type claudeBlock struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input struct {
		Command     string `json:"command"`
		FilePath    string `json:"file_path"`
		URL         string `json:"url"`
		Query       string `json:"query"`
		Pattern     string `json:"pattern"`
		Skill       string `json:"skill"`
		Description string `json:"description"`
	} `json:"input"`
}

// toolResultIDs finds the tool_use_id values in a line without decoding
// the tool output, which can be large.
func toolResultIDs(line []byte) []string {
	var ids []string
	marker := []byte(`"tool_use_id":"`)
	for {
		i := bytes.Index(line, marker)
		if i < 0 {
			return ids
		}
		line = line[i+len(marker):]
		if j := bytes.IndexByte(line, '"'); j >= 0 {
			ids = append(ids, string(line[:j]))
			line = line[j:]
		}
	}
}

// parseClaude reads a Claude Code transcript (~/.claude/projects/<dir>/*.jsonl).
// Each assistant message carries the usage of the call that produced it.
// Claude Code writes one line per content block of a response, so lines
// with the same message id are one call.
func parseClaude(path string, r io.Reader) (fileData, error) {
	var fd fileData
	var calls []*call
	byKey := map[string]*call{}
	tools := map[string]toolUse{}
	var pending []toolUse
	fd.sub = strings.Contains(path, "/subagents/")
	err := eachLine(r, func(line []byte) {
		switch {
		case bytes.Contains(line, []byte(`"type":"ai-title"`)):
			var l claudeLine
			if json.Unmarshal(line, &l) == nil && l.AITitle != "" {
				fd.title = l.AITitle
			}
		case bytes.Contains(line, []byte(`"type":"assistant"`)):
			var l claudeLine
			if json.Unmarshal(line, &l) != nil || l.Type != "assistant" {
				return
			}
			if fd.id == "" {
				fd.id = l.SessionID
			}
			var blocks struct {
				Message struct {
					Content []claudeBlock `json:"content"`
				} `json:"message"`
			}
			if bytes.Contains(line, []byte(`"type":"tool_use"`)) {
				json.Unmarshal(line, &blocks)
			}
			for _, b := range blocks.Message.Content {
				if b.Type == "tool_use" {
					in := b.Input
					detail := in.Command + in.FilePath + in.URL + in.Query + in.Pattern + in.Skill
					if detail == "" {
						detail = in.Description
					}
					tools[b.ID] = toolUse{b.Name, short(detail, 300)}
				}
			}
			u := l.Message.Usage
			if u == nil || u.Input+u.Output+u.CacheRead+u.CacheWrite == 0 {
				return
			}
			tokens := Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
			key := ""
			if l.Message.ID != "" {
				key = l.Message.ID + ":" + l.RequestID
				if c := byKey[key]; c != nil {
					if tokens.Total() > c.rec.Total() {
						c.rec.Tokens = tokens
					}
					return
				}
			}
			project := l.Cwd
			if project == "" {
				project = filepath.Base(filepath.Dir(path))
			}
			c := &call{rec: record{key, Usage{
				Tokens: tokens, Time: l.Timestamp, Project: project, Tool: Claude, Model: l.Message.Model,
				Session: path, Version: l.Version, Sub: fd.sub, Context: u.Input + u.CacheRead + u.CacheWrite,
				Cache1h: u.Cache.OneHour,
			}}, tools: pending}
			pending = nil
			calls = append(calls, c)
			if key != "" {
				byKey[key] = c
			}
		case bytes.Contains(line, []byte(`"tool_result"`)):
			for _, id := range toolResultIDs(line) {
				if t, ok := tools[id]; ok {
					pending = append(pending, t)
				}
			}
		}
	})
	fd.records, fd.outputs = finish(calls)
	return fd, err
}

type codexTokens struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
	Total      int64 `json:"total_tokens"`
}

type codexWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

type codexLine struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   struct {
		Type       string          `json:"type"`
		ID         string          `json:"id"`
		Cwd        string          `json:"cwd"`
		Model      string          `json:"model"`
		CLIVersion string          `json:"cli_version"`
		Source     json.RawMessage `json:"source"`
		Name       string          `json:"name"`
		Arguments  json.RawMessage `json:"arguments"`
		Input      json.RawMessage `json:"input"`
		Info       *struct {
			Last codexTokens `json:"last_token_usage"`
		} `json:"info"`
		RateLimits *struct {
			PlanType  string       `json:"plan_type"`
			Primary   *codexWindow `json:"primary"`
			Secondary *codexWindow `json:"secondary"`
		} `json:"rate_limits"`
	} `json:"payload"`
}

// codexDetail returns the command or path of a Codex tool call.
func codexDetail(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		s = string(raw)
	}
	var args struct {
		Cmd     any    `json:"cmd"`
		Command any    `json:"command"`
		Path    string `json:"path"`
		Query   string `json:"query"`
	}
	if json.Unmarshal([]byte(s), &args) != nil {
		return short(strings.TrimPrefix(strings.TrimSpace(s), "*** Begin Patch\n"), 300)
	}
	for _, v := range []any{args.Cmd, args.Command} {
		switch v := v.(type) {
		case string:
			return short(v, 300)
		case []any:
			parts := make([]string, len(v))
			for i, p := range v {
				parts[i] = fmt.Sprint(p)
			}
			return short(strings.Join(parts, " "), 300)
		}
	}
	return short(args.Path+args.Query, 300)
}

// parseCodex reads a Codex rollout (~/.codex/sessions/YYYY/MM/DD/*.jsonl).
// Each token_count event carries the usage of the last call. Codex sometimes
// repeats an event, and a forked or sub-agent session copies its parent's
// events with new timestamps and, in some versions, new session totals. So the
// usage of the call and the plan limits after it are the key that finds
// copies. A call of another session with the same usage usually has other
// limits. The model comes from the turn_context line that starts each turn.
func parseCodex(path string, r io.Reader) (fileData, error) {
	var fd fileData
	var calls []*call
	var pending []toolUse
	project, model, version, lastUsage := "", "", "", ""
	err := eachLine(r, func(line []byte) {
		// The line and payload types come first, and the rest can be large.
		head := line[:min(len(line), 300)]
		isMeta := fd.id == "" && bytes.Contains(head, []byte(`"session_meta"`))
		if !isMeta && !bytes.Contains(head, []byte(`"token_count"`)) && !bytes.Contains(head, []byte(`"turn_context"`)) &&
			!bytes.Contains(head, []byte(`"type":"function_call"`)) && !bytes.Contains(head, []byte(`"type":"custom_tool_call"`)) {
			return
		}
		var l codexLine
		if json.Unmarshal(line, &l) != nil {
			return
		}
		p := l.Payload
		switch {
		case l.Type == "session_meta":
			if fd.id != "" {
				return
			}
			fd.id, project, version = p.ID, p.Cwd, p.CLIVersion
			var src struct {
				Subagent *struct {
					ThreadSpawn struct {
						ParentThreadID string `json:"parent_thread_id"`
					} `json:"thread_spawn"`
				} `json:"subagent"`
			}
			if json.Unmarshal(p.Source, &src) == nil && src.Subagent != nil {
				fd.sub, fd.parent = true, src.Subagent.ThreadSpawn.ParentThreadID
			}
		case l.Type == "turn_context":
			model = p.Model
		case p.Type == "function_call":
			pending = append(pending, toolUse{p.Name, codexDetail(p.Arguments)})
		case p.Type == "custom_tool_call":
			pending = append(pending, toolUse{p.Name, codexDetail(p.Input)})
		case l.Type == "event_msg" && p.Type == "token_count" && p.Info != nil && p.Info.Last.Total > 0:
			last := p.Info.Last
			if fmt.Sprint(last) == lastUsage {
				return // a repeated event
			}
			lastUsage = fmt.Sprint(last)
			key := lastUsage
			var lim *Limit
			if rl := p.RateLimits; rl != nil {
				lim = &Limit{Plan: rl.PlanType}
				for _, w := range []*codexWindow{rl.Primary, rl.Secondary} {
					if w == nil {
						continue
					}
					key += fmt.Sprint(*w)
					switch w.WindowMinutes {
					case 7 * 24 * 60:
						lim.Week, lim.WeekReset, lim.WeekResetBucket = w.UsedPercent, time.Unix(w.ResetsAt, 0), w.ResetsAt/3600
					case 5 * 60:
						lim.Hours5, lim.Hours5Reset = w.UsedPercent, time.Unix(w.ResetsAt, 0)
					}
				}
				if lim.WeekReset.IsZero() {
					lim = nil
				}
			}
			calls = append(calls, &call{rec: record{key, Usage{
				Tokens: Tokens{Input: last.Input - last.Cached, Output: last.Output, CacheRead: last.Cached},
				Time:   l.Timestamp, Project: project, Tool: Codex, Model: model, Session: path,
				Version: version, Sub: fd.sub, Context: last.Input, Limit: lim,
			}}, tools: pending})
			pending = nil
		}
	})
	fd.records, fd.outputs = finish(calls)
	return fd, err
}

// dedupe keeps one record per key. Claude Code writes one line per content
// block of a response, and later lines can carry a higher output count, so
// the largest usage wins. A copy in a resumed or forked session is never
// older than the original call, so the rest of the earliest record wins.
func dedupe(recs []record) []Usage {
	out := make([]Usage, 0, len(recs))
	seen := make(map[string]int, len(recs))
	for _, r := range recs {
		if r.key == "" {
			out = append(out, r.Usage)
			continue
		}
		if i, ok := seen[r.key]; ok {
			kept := &out[i]
			tokens := kept.Tokens
			if r.Total() > kept.Total() {
				tokens = r.Tokens
			}
			if r.Time.Before(kept.Time) {
				*kept = r.Usage
			}
			kept.Tokens = tokens
			continue
		}
		seen[r.key] = len(out)
		out = append(out, r.Usage)
	}
	return out
}

// dedupeOutputs keeps the earliest copy of each output, like dedupe.
func dedupeOutputs(outs []Output) []Output {
	kept := make([]Output, 0, len(outs))
	seen := make(map[string]int, len(outs))
	for _, o := range outs {
		if o.key == "" {
			kept = append(kept, o)
			continue
		}
		if i, ok := seen[o.key]; ok {
			if o.Time.Before(kept[i].Time) {
				kept[i] = o
			}
			continue
		}
		seen[o.key] = len(kept)
		kept = append(kept, o)
	}
	return kept
}

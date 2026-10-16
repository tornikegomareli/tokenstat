package main

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Copilot Chat in VS Code keeps each chat in a chatSessions folder of the
// workspace storage. A .jsonl file is an operation log: kind 0 holds the
// whole session, kind 1 sets the value at the path k, and kind 2 adds items
// to the array at k (from index i, when given). An older .json file holds
// the whole session.
//
// A request is one prompt, which can run several model calls ("tool call
// rounds"). completionTokens is the output of all the rounds so far, but
// promptTokens is the prompt of the latest round only, and not every round
// writes it. So the input is estimated as the mean prompt that was written
// times the number of rounds. The logs do not split the prompt into cache
// reads and new input, so all of it counts as input.

// vscodeRoots returns the folders where VS Code keeps chat sessions: under
// ~/Library/Application Support on macOS, ~/.config on Linux, and
// %AppData% on Windows.
func vscodeRoots() []string {
	config, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	var roots []string
	for _, app := range []string{"Code", "Code - Insiders"} {
		u := filepath.Join(config, app, "User")
		roots = append(roots, filepath.Join(u, "workspaceStorage"), filepath.Join(u, "globalStorage", "emptyWindowChatSessions"))
	}
	return roots
}

// findChatSessions returns the session files under the roots that exist.
func findChatSessions(roots ...string) []string {
	var files []string
	for _, root := range roots {
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := filepath.Ext(path)
			dir := filepath.Base(filepath.Dir(path))
			if (ext == ".jsonl" || ext == ".json") && (dir == "chatSessions" || dir == "emptyWindowChatSessions") {
				files = append(files, path)
			}
			return nil
		})
	}
	return files
}

type vscodeRequest struct {
	RequestID        string  `json:"requestId"`
	Timestamp        int64   `json:"timestamp"` // ms since 1970
	ModelID          string  `json:"modelId"`
	PromptTokens     float64 `json:"promptTokens"`
	CompletionTokens float64 `json:"completionTokens"`
	Result           *struct {
		Metadata struct {
			ResolvedModel  string            `json:"resolvedModel"`
			PromptTokens   float64           `json:"promptTokens"`
			OutputTokens   float64           `json:"outputTokens"`
			ToolCallRounds []json.RawMessage `json:"toolCallRounds"`
		} `json:"metadata"`
	} `json:"result"`

	prompts []float64 // every promptTokens the log wrote, in order
}

type vscodeSession struct {
	Requests []*vscodeRequest `json:"requests"`
}

type vscodeOp struct {
	Kind int             `json:"kind"`
	K    []any           `json:"k"`
	V    json.RawMessage `json:"v"`
	I    *int            `json:"i"`
}

func parseVSCode(path string, r io.Reader) (fileData, error) {
	var fd fileData
	var s vscodeSession
	if filepath.Ext(path) == ".json" {
		if err := json.NewDecoder(r).Decode(&s); err != nil {
			return fd, err
		}
	} else if err := eachLine(r, func(b []byte) { s.apply(b) }); err != nil {
		return fd, err
	}

	project := vscodeProject(path)
	for _, q := range s.Requests {
		if q == nil || q.Timestamp == 0 {
			continue
		}
		rounds, output := 1, q.CompletionTokens
		model := strings.TrimPrefix(q.ModelID, "copilot/")
		if m := q.Result; m != nil {
			rounds = max(len(m.Metadata.ToolCallRounds), 1)
			if m.Metadata.ResolvedModel != "" {
				model = m.Metadata.ResolvedModel
			}
			if output == 0 {
				output = m.Metadata.OutputTokens
			}
		}
		prompts := q.prompts
		if len(prompts) == 0 && q.PromptTokens > 0 {
			prompts = []float64{q.PromptTokens}
		}
		if len(prompts) == 0 && q.Result != nil && q.Result.Metadata.PromptTokens > 0 {
			prompts = []float64{q.Result.Metadata.PromptTokens}
		}
		var sum float64
		for _, p := range prompts {
			sum += p
		}
		input := 0.0
		if len(prompts) > 0 {
			input = sum / float64(len(prompts)) * float64(max(rounds, len(prompts)))
		}
		if input+output == 0 {
			continue
		}
		if model == "" || model == "auto" {
			model = "copilot-auto"
		}
		key := ""
		if q.RequestID != "" {
			key = "vscode:" + q.RequestID
		}
		fd.records = append(fd.records, record{key, Usage{
			Tokens: Tokens{Input: int64(input + 0.5), Output: int64(output + 0.5)},
			Time:   time.UnixMilli(q.Timestamp), Project: project, Tool: Copilot, Model: model,
			Session: path, Calls: max(rounds, len(prompts)),
		}})
	}
	return fd, nil
}

// apply replays one line of a .jsonl session log. Only the requests and
// their token fields matter.
func (s *vscodeSession) apply(b []byte) {
	var op vscodeOp
	if json.Unmarshal(b, &op) != nil {
		return
	}
	switch {
	case op.Kind == 0:
		var snap vscodeSession
		if json.Unmarshal(op.V, &snap) == nil {
			*s = snap
		}
	case op.Kind == 2 && len(op.K) == 1 && op.K[0] == "requests":
		var add []*vscodeRequest
		if json.Unmarshal(op.V, &add) != nil {
			return
		}
		if op.I != nil && *op.I >= 0 && *op.I < len(s.Requests) {
			s.Requests = s.Requests[:*op.I]
		}
		s.Requests = append(s.Requests, add...)
	case op.Kind == 1 && len(op.K) == 3 && op.K[0] == "requests":
		n, ok := op.K[1].(float64)
		if !ok || int(n) < 0 || int(n) >= len(s.Requests) || s.Requests[int(n)] == nil {
			return
		}
		q := s.Requests[int(n)]
		switch op.K[2] {
		case "promptTokens":
			var v float64
			if json.Unmarshal(op.V, &v) == nil {
				q.PromptTokens = v
				q.prompts = append(q.prompts, v)
			}
		case "completionTokens":
			json.Unmarshal(op.V, &q.CompletionTokens)
		case "result":
			json.Unmarshal(op.V, &q.Result)
		case "modelId":
			json.Unmarshal(op.V, &q.ModelID)
		}
	}
}

// vscodeProject returns the folder of the workspace that a session belongs
// to, from the workspace.json next to its chatSessions folder.
func vscodeProject(path string) string {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(path)), "workspace.json"))
	if err != nil {
		return "VS Code"
	}
	var w struct {
		Folder    string `json:"folder"`
		Workspace string `json:"workspace"`
	}
	if json.Unmarshal(b, &w) != nil {
		return "VS Code"
	}
	for _, s := range []string{w.Folder, w.Workspace} {
		if u, err := url.Parse(s); err == nil && u.Scheme == "file" && u.Path != "" {
			p := u.Path
			if len(p) > 2 && p[0] == '/' && p[2] == ':' { // /c:/Users/... on Windows
				p = p[1:]
			}
			return strings.TrimSuffix(p, ".code-workspace")
		}
	}
	return "VS Code"
}

package main

import (
	"encoding/json"
	"io"
	"path/filepath"
	"time"
)

type copilotLine struct {
	Type      string    `json:"type"`
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Data      struct {
		SessionID      string `json:"sessionId"`
		CopilotVersion string `json:"copilotVersion"`
		Context        struct {
			Cwd string `json:"cwd"`
		} `json:"context"`
		ModelMetrics       copilotMetrics `json:"modelMetrics"` // session.shutdown
		AccountingSnapshot struct {
			ModelMetrics copilotMetrics `json:"modelMetrics"` // session.usage_checkpoint
		} `json:"accountingSnapshot"`
	} `json:"data"`
}

type copilotMetrics map[string]struct {
	Requests struct {
		Count int `json:"count"`
	} `json:"requests"`
	Usage struct {
		Input      int64 `json:"inputTokens"` // includes cache reads
		Output     int64 `json:"outputTokens"`
		CacheRead  int64 `json:"cacheReadTokens"`
		CacheWrite int64 `json:"cacheWriteTokens"`
	} `json:"usage"`
}

type copilotTotal struct {
	Tokens
	calls int
}

func parseCopilot(path string, r io.Reader) (fileData, error) {
	var fd fileData
	if filepath.Base(path) != "events.jsonl" {
		return fd, nil
	}
	fd.id = filepath.Base(filepath.Dir(path))
	project, version := "", ""
	prev := map[string]copilotTotal{} // by model, at the last shutdown
	err := eachLine(r, func(b []byte) {
		var l copilotLine
		if json.Unmarshal(b, &l) != nil {
			return
		}
		switch l.Type {
		case "session.start", "session.resume":
			if l.Data.Context.Cwd != "" {
				project = l.Data.Context.Cwd
			}
			if l.Data.CopilotVersion != "" {
				version = l.Data.CopilotVersion
			}
		case "session.shutdown", "session.usage_checkpoint":
			metrics := l.Data.ModelMetrics
			if l.Type == "session.usage_checkpoint" {
				metrics = l.Data.AccountingSnapshot.ModelMetrics
			}
			for model, m := range metrics {
				u := m.Usage
				now := copilotTotal{Tokens{max(u.Input-u.CacheRead, 0), u.Output, u.CacheRead, u.CacheWrite}, m.Requests.Count}
				p := prev[model]
				if now.Total() < p.Total() {
					p = copilotTotal{}
				}
				prev[model] = now
				d := Tokens{
					Input:      max(now.Input-p.Input, 0),
					Output:     max(now.Output-p.Output, 0),
					CacheRead:  max(now.CacheRead-p.CacheRead, 0),
					CacheWrite: max(now.CacheWrite-p.CacheWrite, 0),
				}
				if d.Total() == 0 {
					continue
				}
				fd.records = append(fd.records, record{path + ":" + l.ID + ":" + model, Usage{
					Tokens: d, Time: l.Timestamp, Project: project, Tool: Copilot, Model: model,
					Session: path, Version: version, Calls: max(now.calls-p.calls, 1),
				}})
			}
		}
	})
	if project == "" {
		project = "~/.copilot"
	}
	for i := range fd.records {
		if fd.records[i].Project == "" {
			fd.records[i].Project = project
		}
	}
	return fd, err
}

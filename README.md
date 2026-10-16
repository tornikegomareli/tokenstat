<h1 align="center">tokenstat</h1>

<h3 align="center">Check your spending tokens in beautiful way</h3>

<p align="center">
  <img src=".github/assets/dashboard.png" width="100%" alt="The tokenstat dashboard: tokens by type, a daily bar chart for Claude Code and Codex, the top projects, and the top models" />
</p>

tokenstat is a terminal dashboard for the tokens that Claude Code, Codex, and GitHub Copilot (CLI and VS Code) use. It reads the session logs on your machine, so it needs no account and sends nothing anywhere. Pick a date range, a tool, a project, or a model, and every panel follows.

## Filter everything

Press `d` to switch between today, 7, 14, 30, and 90 days, or `c` to type your own range. Press `t` for one tool, `p` for one project, and `m` for one model. The project and model pickers search as you type.

The chart shows one bar per day. When the range has more days than the window has rows, it shows weeks, then months.

## Find out who used your Codex limit

Codex writes your plan limit percentage with every call. tokenstat gives each percent point to the call that used it, so you see which projects used your weekly limit and where the week ends at your current pace.

It also shows how many tokens 1% of the limit bought each week. If that number jumps, the limit changed.

<p align="center">
  <img src=".github/assets/limits.png" width="100%" alt="Limit forensics: the weekly limit now, the points each project used, and tokens per 1% of the limit for each week" />
</p>

## Explain a spike

Pick a day to compare it with the median day of the range. tokenstat names the sessions that used the most, the context size of the calls, cache rebuilds after idle time, the share of subagents, the heaviest tool output, and the models.

<p align="center">
  <img src=".github/assets/spikes.png" width="100%" alt="Spike explainer: a list of days, and the reasons one day used more tokens than the median day" />
</p>

## See what each tool output costs

A tool output stays in the context, so every later call sends it again until a compaction removes it. tokenstat calls this rent: the tokens an output added, times the calls that sent it again. It ranks tools and single outputs, with the command or file behind each one.

<p align="center">
  <img src=".github/assets/rent.png" width="100%" alt="Context rent: rent by tool, and the costliest single outputs with their commands and files" />
</p>

## Watch a session's context grow

Pick a session to see its context size from the first call to the last. Marks show cache rebuilds, compactions, subagent results, and large outputs, and the event list says what happened at each one.

<p align="center">
  <img src=".github/assets/timeline.png" width="100%" alt="Session timeline: context size over one session, with marks for cache rebuilds, compactions, and subagents" />
</p>

## Catch changes between versions

For each Claude Code and Codex version, tokenstat shows the context sent before the first reply. For Claude Code it also shows the share of cache writes kept for one hour; the rest expire after five minutes. A jump in either means a new version costs you more.

<p align="center">
  <img src=".github/assets/drift.png" width="100%" alt="Version drift: start-up context and cache lifetime for each agent version" />
</p>

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/tornikegomareli/tokenstat/main/install.sh | sh
```

The script downloads the latest release for your system, checks its SHA-256 checksum, and puts `tokenstat` in `~/.local/bin`. To install somewhere else, set `TOKENSTAT_INSTALL_DIR`:

```sh
curl -fsSL https://raw.githubusercontent.com/tornikegomareli/tokenstat/main/install.sh | TOKENSTAT_INSTALL_DIR="$HOME/bin" sh
```

On Windows, run this in PowerShell. It installs `tokenstat.exe` to `%LOCALAPPDATA%\Programs\tokenstat` and adds that folder to your PATH:

```powershell
irm https://raw.githubusercontent.com/tornikegomareli/tokenstat/main/install.ps1 | iex
```

With Go 1.25 or newer, you can also build it yourself:

```sh
go install github.com/tornikegomareli/tokenstat@latest
```

Requirements:

- macOS, Linux, or Windows, on arm64 or x86-64. On Windows, use Windows Terminal or PowerShell; the logs are in the same folders under `%USERPROFILE%`, and VS Code's under `%APPDATA%\Code`.
- Logs from Claude Code (`~/.claude/projects`), Codex (`~/.codex/sessions`, or `$CODEX_HOME/sessions`), Copilot CLI (`~/.copilot/session-state`), or Copilot Chat in VS Code (the `chatSessions` folders of its workspace storage).
- A terminal window of at least 80 × 24.

## Using it

Run `tokenstat`.

- `d` switches the date range. `c` sets a custom range, such as `2026-07-01 2026-09-30`. With one date, the range ends today.
- `t` switches between all tools, Claude Code, and Codex.
- `p` picks a project. Type to search, use ↑↓ to move, and Enter to select.
- `m` picks a model the same way.
- `a` opens the advanced views. Use ↑↓ and Enter to open one, and Esc to go back.
- `s` sends your daily totals to the leaderboard, like `tokenstat submit`. If you are not logged in, it starts the GitHub login first and shows the code to enter. The top line shows the account you are logged in as.
- `r` resets the filters. `q` quits.

Set `NO_COLOR=1` to turn off colors.

## Leaderboard

You can put your totals on the public leaderboard:

```sh
tokenstat login    # sign in with GitHub
tokenstat submit   # send daily totals
```

If you are not logged in, `submit` runs the login first. It sends one row per day, tool, and model: the token counts and the number of calls. It does not send projects, paths, sessions, commands, or titles. Run `tokenstat submit -dry-run` to see the rows first. `tokenstat logout` forgets the login. Set `TOKENSTAT_SERVER` to use another leaderboard.

## How it counts

- Each model call counts once. Claude Code writes one line for each part of a reply, and resumed or forked sessions copy old lines. tokenstat keeps one copy of each call.
- The numbers are tokens: input, output, cache reads, and cache writes. They are not dollars.
- tokenstat sees only the logs that are still on disk. Claude Code deletes old transcripts after the number of days in its `cleanupPeriodDays` setting.
- Codex limit points also include Codex use on other devices and on the web.
- Claude Code logs do not record plan limits, so the limit view covers Codex only.
- Copilot CLI, and VS Code's Copilot agent, log a session's tokens so far every minute or two while it runs, and again when it ends or pauses. Each snapshot counts the tokens added since the one before, on the day it was written. The logs have no tokens per call, so the spike, session, timeline, and drift views leave Copilot out.
- VS Code writes the prompt size of the latest model call of a request, and not for every call. tokenstat counts the mean written prompt times the calls of the request. VS Code does not split prompts into cache reads and new input, so for VS Code all of it counts as input.

## Remove

```sh
rm ~/.local/bin/tokenstat
```

## Development

From a clone of this repository:

```sh
go test ./...
go run .
```

Pushing a `v*` tag builds the release with GoReleaser.

## License

[MIT License](LICENSE).

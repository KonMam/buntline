# Contributing

Thanks for your interest in buntline. Issues and pull requests are welcome.

## Setup

You need Go 1.25+, Node 22+, and ripgrep.

```bash
git clone https://github.com/KonMam/buntline
cd buntline && cd web && npm ci && cd ..
make build          # embedded binary at bin/buntline
```

## Development loop

The backend and frontend run separately during development:

```bash
go run ./cmd/buntline -no-open    # Go API on :7433 (serves web/dist if built)
cd web && npm run dev           # Vite on :5173 with HMR, proxies /api
```

`make dev` prints the same two commands.

## Before you open a PR

```bash
make check          # gofmt + golangci-lint + tests (race) + embedded build
lefthook install    # optional: the same fast checks as a pre-commit hook
```

`make check` is exactly what CI runs. A green local run means a green PR.
Live-backend tests (`make integration`) hit real endpoints and skip cleanly
when a backend is unreachable. Run them when your change touches the agent
loop, tools, or provider code.

## Conventions

- Tests accompany breakage-prone code: agent loop, provider parsing, tools,
  MCP client, config. Failures surface visibly in the trace; nothing is
  swallowed.
- One round of related work per commit. The message explains why, not what.
- Keep the system prompt small and stable; it is the cacheable prefix of
  every request. Per-repo context belongs in `AGENTS.md` files.
- UI copy: plain sentences, no filler.

## Layout

- `cmd/buntline`: entrypoint, flags, server and headless startup, shutdown
- `internal/agent`: the loop. Rounds, tool calls, approvals, steering,
  loop detection, tool-call repair, compaction
- `internal/provider`: OpenAI-compatible streaming client (SSE). One
  adapter covers Ollama, vLLM, llama.cpp, DeepSeek, and friends
- `internal/server`: HTTP and SSE for the UI. Session lifecycle,
  approval round-trips, auto-compaction, session search
- `internal/session`: on-disk store. Per session: meta.json,
  transcript.jsonl, events.jsonl
- `internal/tools`: built-ins. Read/write/edit file, bash, background
  tasks, grep, glob. Workdir confinement for file paths
- `internal/module`: the registry. Modules contribute tools, HTTP routes,
  interceptors, observers; toggleable at runtime
- `internal/config`: config.toml, profiles, per-repo settings, secret
  references
- `web/src`: Svelte 5 with runes. `lib/` holds the api client and session
  store, `components/` the UI

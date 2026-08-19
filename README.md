# buntline

[![ci](https://github.com/KonMam/buntline/actions/workflows/ci.yml/badge.svg)](https://github.com/KonMam/buntline/actions/workflows/ci.yml)
[![license](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A fast harness for self-hosted and API LLMs. One Go binary runs an agent
loop (tools, permissions, sessions) and serves a browser UI on localhost.

## Features

- **Agent loop** against any OpenAI-compatible endpoint (Ollama, vLLM,
  llama.cpp, LM Studio, OpenRouter, DeepSeek, ...).
- **Tools**: `read_file`, `write_file`, `edit_file` (exact string replace),
  `bash`, `grep` (ripgrep), `glob`. All confined to the working directory.
- **Permissions**: read-only tools run freely. Side-effectful tools pause
  the loop for browser approval (allow once, allow for session, deny).
- **Everything visible**: an activity log records every model call, tool
  run (args, result, duration), approval, and per-turn token usage,
  including provider cache hits.
- **Notifications**: approvals, questions, turn ends, and errors from
  *any* session raise an in-app bell and OS-level popups while the UI is
  open — on desktop and mobile (install the app to the home screen on
  iOS for native notifications). Per-type settings live in the Notifications
  view under Modules. See [docs/linux.md](docs/linux.md).
- **Sessions** persist as plain JSONL under
  `~/.local/share/buntline/sessions/`. Resume from the sidebar.
- **`/compact`** summarizes and resets the transcript explicitly, because
  rewriting history invalidates prefix caching. The stats show what it
  costs.
- **Headless mode**: `buntline -p "prompt"` streams events as JSONL to
  stdout.

## Install

Prerequisites: ripgrep, and an OpenAI-compatible server to talk to (e.g.
`ollama serve`).

**From a release**: download the archive for your platform from
[Releases](https://github.com/KonMam/buntline/releases), unpack, and put
`buntline` on your PATH. Then:

```bash
buntline        # serves http://localhost:7433 and opens the browser
```

**From source** (needs Go 1.25+ and Node 22+):

```bash
make build          # builds the web UI and embeds it into bin/buntline
./bin/buntline
```

**Docker**: images for amd64/arm64 are on
[ghcr.io/konmam/buntline](https://ghcr.io/konmam/buntline); see
[docs/docker.md](docs/docker.md) for the compose setup.

Running it on a home server? See [docs/linux.md](docs/linux.md) for the
systemd unit and remote-access setup, or run the container.

## Configuration

Configuration precedence: flags > `BUNTLINE_*` env vars > `./buntline.toml` >
`~/.config/buntline/config.toml`.

```toml
# buntline.toml
base_url = "http://localhost:11434/v1"
model    = "qwen3.5:9b"

# Paid OpenAI-compatible providers as profiles, switchable per session
# from the composer. Keys reference env vars so nothing secret lives in
# the file. Check each provider's docs for current model ids.
[[profiles]]
name     = "deepseek"
base_url = "https://api.deepseek.com/v1"
model    = "deepseek-chat"
api_key  = "${DEEPSEEK_API_KEY}"

[[profiles]]
name     = "kimi"
base_url = "https://api.moonshot.ai/v1"
model    = "kimi-k2.5"
api_key  = "${MOONSHOT_API_KEY}"
```

The prompt has two levels: a minimal global system prompt (view and edit
via the "prompt" button, stored at `~/.config/buntline/system.md`) and
per-directory project instructions from `AGENTS.md` (or `CLAUDE.md`),
injected into the conversation and visible as a collapsed chip.

## Security model

buntline runs shell commands, so know where the boundaries are:

- File tools are confined to the session's working directory.
  Side-effectful tools, including every `bash` command, stop the loop
  for approval unless allowlisted per repository.
- The server binds `localhost` by default and rejects requests with a
  foreign `Host` or `Origin` header (DNS rebinding, cross-site
  requests).
- There is **no authentication**. Anyone who can reach the port can
  drive the agent. For remote use, put it behind a VPN (see
  [docs/linux.md](docs/linux.md)). Never expose the port to the
  internet.
- API keys entered in the UI go to the macOS Keychain, or a `0600` file
  on Linux. Keys in config files are env-var references, never values.

See [SECURITY.md](SECURITY.md) for reporting vulnerabilities.

## Development

```bash
go run ./cmd/buntline -no-open    # API on :7433 (serves web/dist if built)
cd web && npm run dev           # Vite dev server on :5173, proxies /api
make check                      # fmt + lint + tests + release build
```

Layout: `internal/provider` (OpenAI-compatible SSE client), `internal/agent`
(the loop and events), `internal/tools`, `internal/session` (JSONL store),
`internal/server` (REST and SSE), `web/` (Svelte 5 UI). See
[CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow.

## License

[MIT](LICENSE)

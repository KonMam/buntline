# Running tether in Docker

Images are published to `ghcr.io/konmam/tether` for amd64 and arm64, so a
Raspberry Pi (64-bit OS) works the same as a server. A ready-made stack is
in [deploy/compose.yaml](../deploy/compose.yaml):

```bash
mkdir tether && cd tether
curl -LO https://raw.githubusercontent.com/KonMam/tether/main/deploy/compose.yaml
docker compose up -d
```

## What the container changes

The agent's shell and file tools run inside the container, so the agent
sees the container's filesystem, not the host's. Mount what it should
work on at `/workspace`. The image ships the tools the agent shells out
to (bash, git, ripgrep); if your sessions need more (compilers, npm),
extend the image:

```dockerfile
FROM ghcr.io/konmam/tether:latest
RUN apt-get update && apt-get install -y golang nodejs && rm -rf /var/lib/apt/lists/*
```

## State

Everything lives in one volume, mounted at `/data`:

- `/data/sessions`: session transcripts (JSONL)
- `/data/.config/tether/`: config.toml, secrets, system prompt
- `/data/modules.json`: module toggles

## Providers

Set the default provider with `TETHER_BASE_URL`, `TETHER_MODEL`, and
`TETHER_API_KEY` (see the compose example), or manage providers in the
UI's Models view. Local Ollama is optional everywhere: without it the
Models view shows local providers as unavailable and everything else
works. To use models from an Ollama on another machine, point
`TETHER_BASE_URL` at it.

## Reverse proxy

The container listens on `0.0.0.0:7433` (inside the compose network
only; no host port is published). Point your proxy at
`tether:7433` and set `TETHER_ALLOWED_HOSTS` to the hostname you serve
it under, or the DNS-rebinding guard will reject the requests. Caddy
example:

```
tether.example.com {
    reverse_proxy tether:7433
}
```

The same warning as everywhere else applies: tether has no
authentication, so the hostname must only be reachable from a network
you trust (LAN-only DNS, VPN). Never expose it to the internet.

# Running tether on a Linux server

tether is a single binary. A home-server install is: put the binary in
place, run it as a service, reach it over your VPN.

## Install

Download the latest release for your architecture and unpack it:

```bash
curl -LO https://github.com/KonMam/tether/releases/latest/download/tether_<version>_linux_amd64.tar.gz
tar xzf tether_<version>_linux_amd64.tar.gz
sudo install tether /usr/local/bin/
```

Install ripgrep (the `grep` tool shells out to it):

```bash
sudo apt install ripgrep    # Debian/Ubuntu; dnf/pacman equivalents exist
```

If you run models locally, install [Ollama](https://ollama.com) on the
same machine. tether's default config points at
`http://localhost:11434/v1`.

## Run as a service

A dedicated user keeps the agent's shell access scoped to its own home
directory tree:

```bash
sudo useradd -m -s /bin/bash tether
sudo cp deploy/tether.service /etc/systemd/system/
sudo systemctl enable --now tether
journalctl -u tether -f        # watch the log
```

Configuration lives in `/home/tether/.config/tether/config.toml`. The
same `TETHER_*` environment variables and flags work as anywhere else.

## Remote access

tether has **no authentication of its own**. The permission prompts
gate what the agent may do, not who may reach the UI. Anyone who can
open the page can drive an agent that runs shell commands, so reach it
through a VPN (Tailscale, WireGuard, ...), never by exposing the port.

Two ways to wire it up, with Tailscale as the example:

- **Bind the VPN address.** Start tether with
  `-addr 100.x.y.z:7433` (your machine's Tailscale IP). Only VPN peers
  can connect.
- **Bind loopback, forward over the VPN.** Keep the default
  `127.0.0.1:7433` and use `tailscale serve` (which also gives you
  HTTPS) or an SSH tunnel.

If you open the UI via a DNS name (e.g. Tailscale MagicDNS:
`http://box.tailnet-name.ts.net:7433`), add that name to the config so
the DNS-rebinding guard recognizes it:

```toml
# config.toml
allowed_hosts = ["box.tailnet-name.ts.net"]
```

Plain IPs, `localhost`, and the address you bound need no entry.

One caveat when Ollama runs on a different machine than tether: vision
support is detected by recognizing localhost Ollama endpoints, so a
remote Ollama is treated as text-only.

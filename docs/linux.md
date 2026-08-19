# Running buntline on a Linux server

buntline is a single binary. A home-server install is: put the binary in
place, run it as a service, reach it over your VPN.

## Install

Download the latest release for your architecture and unpack it:

```bash
curl -LO https://github.com/KonMam/buntline/releases/latest/download/buntline_<version>_linux_amd64.tar.gz
tar xzf buntline_<version>_linux_amd64.tar.gz
sudo install buntline /usr/local/bin/
```

Install ripgrep (the `grep` tool shells out to it):

```bash
sudo apt install ripgrep    # Debian/Ubuntu; dnf/pacman equivalents exist
```

If you run models locally, install [Ollama](https://ollama.com) on the
same machine. buntline's default config points at
`http://localhost:11434/v1`.

## Run as a service

A dedicated user keeps the agent's shell access scoped to its own home
directory tree:

```bash
sudo useradd -m -s /bin/bash buntline
sudo cp deploy/buntline.service /etc/systemd/system/
sudo systemctl enable --now buntline
journalctl -u buntline -f        # watch the log
```

Configuration lives in `/home/buntline/.config/buntline/config.toml`. The
same `BUNTLINE_*` environment variables and flags work as anywhere else.

## Remote access

buntline has **no authentication of its own**. The permission prompts
gate what the agent may do, not who may reach the UI. Anyone who can
open the page can drive an agent that runs shell commands, so reach it
through a VPN (Tailscale, WireGuard, ...), never by exposing the port.

Two ways to wire it up, with Tailscale as the example:

- **Bind the VPN address.** Start buntline with
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

One caveat when Ollama runs on a different machine than buntline: vision
support is detected by recognizing localhost Ollama endpoints, so a
remote Ollama is treated as text-only.

## Notifications on mobile (and other browsers)

buntline raises in-app and OS-level notifications while the UI is open:
approvals and questions from any session (even one you are not looking
at), turn completions, and errors. The bell in the chat header lists
them. Notifications are a module: under **Modules**, the Notifications
card opens the settings view, where each browser remembers its own
choices (per-type toggles, desktop popups on/off) in `localStorage`, and
the module switch turns the whole feature off.

OS-level popups need a **secure context**, which means:

- `http://localhost:7433` works as-is (loopback counts as secure).
- A VPN name reached over plain HTTP (`http://box.tailnet.ts.net:7433`)
  does **not** show popups. Use `tailscale serve` (which gives HTTPS),
  or another reverse proxy with TLS. The in-app bell and banner still
  work over plain HTTP.
- On **iOS Safari**, `Notification.requestPermission` only works after
  you add the page to your home screen (iOS 16.4+; earlier iOS versions
  never show web notifications). With the manifest buntline ships, "Add
  to Home Screen" also gives the app its own icon and standalone mode.
  Chrome/Android and desktop browsers work without installation.

Multiple open tabs never double popups: one tab is elected to raise
them, and the in-app list is shared per tab.

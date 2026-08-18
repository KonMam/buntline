# Security policy

## Reporting a vulnerability

Report vulnerabilities privately via
[GitHub security advisories](https://github.com/KonMam/tether/security/advisories/new).
Please do not open public issues for security problems.

## Scope

tether executes shell commands and edits files by design; what counts as a
vulnerability is a break in its stated boundaries:

- File-tool access escaping the session's working directory.
- Side-effectful tools running without an approval (outside an explicit
  allowlist or `-auto-approve`).
- The browser boundary failing: DNS-rebinding or cross-site requests
  passing the Host/Origin guard.
- Secret values (API keys) being exposed through the API or UI.

The absence of authentication on the HTTP server is a documented design
decision, not a vulnerability: the server is meant to be reached over
localhost or a VPN only. See the security model section in the README.

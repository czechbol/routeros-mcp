# CLAUDE.md

This file provides guidance to Claude Code when working with code in this repository.

## What this is

`routeros-mcp` — single-binary MCP server (Go 1.26, module `github.com/czechbol/routeros-mcp`)
that exposes the MikroTik RouterOS REST API to LLMs via generic tools
(`ros_print`, `ros_add`, `ros_set`, `ros_remove`, `ros_exec`, `ros_list_paths`,
`ros_describe`). Ships as a ~14 MB scratch container; designed to also run
inside RouterOS' own `container` feature.

Transport: streamable HTTP MCP on `/mcp` (bearer-token guarded), `/healthz`
on the same listener. See `README.md` and `docs/user-guide.md` for ops; see
`docs/contributing.md` for senior-eng-level engineering notes (read it — it
is the authoritative dev guide).

## Build / lint / test

`mage` is the only build entry point (no `Makefile`). Targets defined in
`magefile.go` (build tag `//go:build mage`):

```sh
mage -l           # list targets
mage check        # format + lint + test (CI gate)
mage test         # go test -race -cover ./...
mage lint         # golangci-lint v2.12.2, auto-installed under ./bin/
mage build        # host-arch binary -> dist/
mage buildall     # cross-compile all supported arches
mage tarballs     # per-arch OCI tarballs (no push) -> dist/
mage release      # multi-arch push + tarballs (env-driven, CI)
mage shards       # regenerate tools/openapi/*.json from mikrotik-openapi.json
```

Single test: `go test -run TestName ./tools/...`.

Lint is strict (`.golangci.yml`): cyclop ≤ 10, gocognit, revive `add-constant`,
gosec, `err113` (every dynamic `fmt.Errorf` must wrap a sentinel),
`nolintlint`. Don't append `//nolint` without a reason.

`go.mod` Go version must stay ≤ the toolchain baked into the pinned
golangci-lint release — bump both together.

## Architecture

Entry: `main.go` wires config, MCP server, tool registration, auth/origin
guards, and kicks off live-spec resolution in a goroutine.

```
main.go              entrypoint, HTTP transport, auth + origin guards
server/
  server.go          NewMCP factory
  client.go          basic-auth HTTP client for RouterOS REST
  openapi.go         dynamic OpenAPI fetch + on-disk cache + sentinels
  redact.go          recursive JSON walker, default-deny field allowlist
  format.go          Render(format, out, markdownFn)
  errors.go          ToolError helper
tools/
  rest.go            ros_print / ros_add / ros_set / ros_remove / ros_exec
  discover.go        ros_list_paths (+ embedded paths.txt, ~92 KB)
  describe.go        ros_describe (live spec first, embedded shards fallback)
  openapi/           per-menu OpenAPI shards (~5.8 MB embedded)
internal/sharder/    offline sharder used by `mage shards`
magefile.go          mage targets
Dockerfile           scratch, multi-stage, multi-arch
docs/                user-guide.md, contributing.md
```

Two error levels: tool-level (bad input, RouterOS 4xx/5xx) → return
`*mcp.CallToolResult{IsError: true}` via `server.ToolError` with `nil` Go
error. Protocol/transport errors → non-nil Go error. Sentinels live in
`server/openapi.go` and `tools/`.

Live OpenAPI: `main.loadLiveSpec` runs async so HTTP server never blocks
on the router. `server.ResolveLiveSpec` fetches from
`tikoci.github.io/restraml/<version>/openapi.json` (retry × 3) with cache;
falls back to embedded RouterOS 7.22.3 shards on `ErrSpecNotPublished` /
`ErrOpenAPIUnavailable` / `DYNAMIC_OPENAPI=0`. Path missing from live
spec also silently falls back — drift is expected. When you add a new
sentinel here, also add a matching `errors.Is` branch in `loadLiveSpec`.

## Runtime env vars

Server: `ROS_URL` (default `https://127.0.0.1`), `ROS_USER`, `ROS_PASS`
(both required), `ROS_INSECURE=1` (skip TLS verify), `MCP_TOKEN`
(required unless `MCP_ALLOW_ANON=1`), `LISTEN_ADDR` (default
`0.0.0.0:8080`), `ALLOWED_ORIGINS` (CSV; empty = no origin check),
`REDACT=0` (disable redaction), `REDACT_EXTRA` (extra fields to mask),
`DYNAMIC_OPENAPI=0` (skip live spec fetch).

Release (`mage release`): `REGISTRY` (default `ghcr.io`), `IMAGE_REPO` or
`$GITHUB_REPOSITORY`, `VERSION` or `$GITHUB_REF_NAME`, `PUSH=0` to skip
registry push.

## Commit style

Strict Conventional Commits. **Single subject line only — no body** unless it's
a `BREAKING CHANGE:` footer trailer.
Types: `feat | fix | docs | style | refactor | test | chore | perf | ci |
build | revert`. Match existing log style: no scope, terse.

`CHANGELOG.md` follows Keep-a-Changelog; populate the `[Unreleased]`
section as you go.

## Gotchas

- **Don't add typed wrappers for individual RouterOS paths.** The
  generic tools + `ros_describe` are the design. Adding e.g.
  `ros_firewall_add` duplicates the catalogue and rots.
- **No backticks in `magefile.go`'s package doc comment** — breaks mage
  codegen with cryptic syntax errors.
- **Every tool input field needs a `jsonschema:"…"` tag** — that text is
  what the agent reads to decide how to call the tool.
- **Output structs use concrete types** (no bare `any` — Claude Code's
  schema validator rejects it). Use `map[string]any` or stricter; see
  `toMap` in `tools/rest.go`.
- **`ToolAnnotations` field types are inconsistent.** `ReadOnlyHint` /
  `IdempotentHint` are plain `bool`; `DestructiveHint` / `OpenWorldHint`
  are `*bool`. Use the local `ptr()` helper.
- **Tool defaults belong in the handler**, not the schema.
- **Don't hand-edit `tools/openapi/*.json`** — regenerate via
  `mage shards` after dropping a fresh `mikrotik-openapi.json` at repo
  root (download from `tikoci.github.io/restraml/<version>/openapi.json`).
  Commit the shards; `mikrotik-openapi.json` is local-only.
- **Sharder uses 0600/0755 perms** to satisfy gosec — keep it that way.
- **RouterOS `container` deploy quirks** (env-list race, veth carrier
  race, DNS) — see `docs/contributing.md` "Gotchas" and
  `docs/user-guide.md` §8.
- **Destructive actions are not server-side gated.** `ros_exec` and
  `ros_remove` carry `DestructiveHint: true` and rely on the client to
  confirm. Run with a bearer token.

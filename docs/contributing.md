# Contributing to routeros-mcp

> Audience: senior engineers. Terse on purpose.

## Repo layout

```
.
├── main.go                 entrypoint, transport wiring, auth guards, env loading
├── server/
│   ├── server.go           mcp.Server factory (NewMCP)
│   ├── client.go           HTTP basic-auth client for RouterOS REST
│   ├── errors.go           ToolError helper
│   ├── format.go           Render(format, out, markdownFn)
│   ├── openapi.go          dynamic OpenAPI fetch, cache, sentinel errors
│   └── redact.go           recursive JSON walker, default-deny field allowlist
├── tools/
│   ├── rest.go             ros_print / ros_add / ros_set / ros_remove / ros_exec
│   ├── discover.go         ros_list_paths + embedded paths.txt
│   ├── describe.go         ros_describe (consults SetLiveSpec first, shards otherwise)
│   ├── paths.txt           92 KB top-level path catalogue (embedded)
│   └── openapi/            per-menu OpenAPI shards (~5.8 MB embedded)
├── internal/sharder/       offline tool used by `mage shards`
├── magefile.go             mage targets (build tag `//go:build mage`)
├── Dockerfile              scratch image, multi-stage, multi-arch
├── .golangci.yml           strict v2 config; CI gate is `mage check`
├── .dockerignore
├── go.mod
└── docs/
```

There are no other build files (no `Makefile`); `mage` is the only entry
point.

## Build & lint

```sh
mage -l         # targets
mage check      # format + lint + test (CI gate)
mage tarballs   # OCI tarballs for arm64 / armv7 / amd64 (local, no push)
mage release    # multi-arch push + tarballs (env-driven; see "Releasing")
mage shards     # regenerate tools/openapi/*.json from mikrotik-openapi.json
```

`mage lint` installs golangci-lint **v2.12.2** on demand under `./bin/`.
Don't change the pinned version without updating CI.

The lint config is intentionally aggressive (cyclop ≤ 10, gocognit on,
revive `add-constant` enforced, gosec on, err113 — i.e. every error
must wrap a sentinel). Conform; do not append `//nolint` without a
reason. `nolintlint` will reject unused suppressions.

## Conventional Commits

A commit-msg hook enforces:

- `<type>(<scope>?): <description>` — single subject line, no body.
- `feat | fix | docs | style | refactor | test | chore | perf | ci | build | revert`
- Bodies are rejected unless the commit is a `BREAKING CHANGE:` (footer
  trailer is allowed).

So: `feat: redact secrets in RouterOS responses by default` ✔.
A multi-line bullet body will be rejected. Match the existing log style:
no scope, terse.

## Adding a new tool

Tools are typed Go functions; the SDK generates input/output JSON
Schema from struct tags. Pattern:

```go
type DoThingIn struct {
    Path   string `json:"path"            jsonschema:"…"`
    Format string `json:"format,omitempty" jsonschema:"json or markdown (default markdown)"`
}

type DoThingOut struct { … }

func RegisterDoThing(srv *mcp.Server, c *server.Client) {
    mcp.AddTool(srv, &mcp.Tool{
        Name:        "ros_do_thing",
        Description: "…",   // shown to the agent — be precise
        Annotations: &mcp.ToolAnnotations{
            ReadOnlyHint:    true,
            IdempotentHint:  true,
            DestructiveHint: ptr(false),
            OpenWorldHint:   ptr(true),
        },
    }, doThing(c))
}
```

Rules of thumb:

- **Every input field needs `jsonschema:"…"`.** That text is what the
  agent reads when deciding how to call the tool. Skipping it leaves
  the field nameless in the schema and the agent has to guess.
- **`ToolAnnotations` field types are inconsistent.** `ReadOnlyHint`
  and `IdempotentHint` are plain `bool`; `DestructiveHint` and
  `OpenWorldHint` are `*bool`. Use the local `ptr()` helper.
- **Output structs must use concrete types.** Claude Code's schema
  validator rejects bare `any` fields in the generated `outputSchema`.
  Use `map[string]any` (or stricter) instead. See the `toMap` helper
  in `tools/rest.go` for the standard conversion.
- **Defaults belong in the handler**, not the schema. Set
  `if in.Format == "" { in.Format = formatMarkdown }`.
- Wire the registration in `main.go` next to `tools.RegisterRESTTools`.

## Errors

Two levels:

- **Tool-level errors** (bad input, RouterOS 4xx/5xx, missing path):
  return `*mcp.CallToolResult{IsError: true}` via `server.ToolError(…)`
  and `nil` Go error. The agent sees the message and recovers.
- **Protocol/transport errors** (the SDK or HTTP machinery itself):
  return a non-nil Go error.

Every dynamic `fmt.Errorf` must wrap a sentinel (`err113`). Add new
sentinels next to the existing ones in `server/openapi.go` or `tools/`.
Naming: `errFooBar` (package-private) or `ErrFooBar` if it crosses
packages.

## Live OpenAPI resolution

`main.loadLiveSpec` runs in a **goroutine** so the HTTP server never
blocks waiting on the router. `server.ResolveLiveSpec` does:

```
DYNAMIC_OPENAPI=0 ──► ErrOpenAPIUnavailable
detect router version (retry × 8, 1-8 s back-off)
  └─ cache hit? use cache (Source="cache")
  └─ fetch tikoci.github.io/restraml/<v>/openapi.json (retry × 3, terminal on 404)
       └─ success → write cache, return (Source="live")
       └─ fail   → ErrOpenAPIUnavailable
```

`ros_describe` calls `activeLiveSpec()` first; if nil it falls back to
the embedded sharded catalogue. A path missing from the live spec also
silently falls back — drift between RouterOS versions is expected.

When you add new sentinel error types here, also add a `case
errors.Is(err, server.ErrFoo):` branch in `main.loadLiveSpec` so the
operator gets a useful log line.

## Sharding workflow

`tools/openapi/*.json` is generated from a local
`mikrotik-openapi.json` (~13 MB). Don't hand-edit shards.

```sh
curl -o mikrotik-openapi.json \
  https://tikoci.github.io/restraml/<version>/openapi.json
mage shards
```

`mage shards` writes `tools/openapi/<menu>.json` (lazy-loaded by
`tools/describe.go`) and `tools/openapi/index.json` (version metadata).
Commit the shards; keep `mikrotik-openapi.json` ignored.

The sharder is in `internal/sharder/`. It uses fixed 0600/0755 perms
because gosec rejects 0644 — keep it that way.

## Testing

Unit tests live next to the code they cover. `mage test` runs
`go test -race -cover ./...`. We aim for tests on:

- Anything that touches secret material (`server/redact_test.go`).
- Pure functions in the request path (`tools/rest_test.go`).
- New error code paths.

The `golangci.yml` exclusion list relaxes `bodyclose, err113,
forcetypeassert, gosec, revive:add-constant` for `_test.go`. Use that
to keep tests readable; don't relax it further.

## Containers

`Dockerfile` is multi-stage, `FROM scratch`, multi-arch via buildx.
`mage image` produces local images per platform; `mage tar` saves them
to OCI tarballs RouterOS can import via `/container/add file=…`.

If you change the `go:embed` payload, double-check the resulting binary
size with `ls -lh dist/routeros-mcp` — embedded shards already cost ~5.8 MB.

## Gotchas worth knowing

- **Container env propagation race.** A container created **before**
  its env list is populated will boot without those vars. Order:
  create env list → add keys → create container → start.
- **Container boot race vs. bridge.** `/container/start` can return
  before the veth has carrier. The live-spec loader retries
  (8 attempts, exponential 1-8 s) for this reason — don't shorten
  the back-off without re-testing on a slow router.
- **Container needs DNS.** RouterOS containers default to whatever DNS
  the image was built with (none, here). Pass `dns=…` at
  `/container/add` time. The user guide covers this in §8.6.
- **golangci-lint Go-version pin.** golangci-lint refuses to lint code
  targeting a Go version newer than the toolchain it was built with.
  Keep `go.mod`'s `go` directive ≤ the version baked into the pinned
  golangci-lint release; bump both together.
- **Generic passthrough is intentional.** Don't add typed wrappers for
  individual RouterOS paths. The whole point of the generic
  tools is that the agent uses `ros_describe` + `ros_list_paths` at
  runtime — adding e.g. `ros_firewall_add` would duplicate the
  catalogue and rot quickly.

## What to do before opening a PR

1. `mage check` — must be clean.
2. `mage tarballs` if you changed anything under `tools/openapi/`,
   `Dockerfile`, or anything build-related.
3. If you added a tool: smoke-test against a real router via Claude
   Code or `curl`. Schema-only validation isn't enough.
4. If you changed redaction: re-run `server/redact_test.go` and
   eyeball a `/ppp/secret` or `/interface/wireguard/peers` response
   from a real router.
5. Single Conventional Commits subject. No body unless it's a breaking
   change with a `BREAKING CHANGE:` footer.

## Releasing

All release logic lives in **mage**; `.github/workflows/release.yml` is
a thin wrapper that authenticates and invokes `mage release`.

`mage release` (env-driven):

| Var          | Default                                  | Notes                                                                            |
| ------------ | ---------------------------------------- | -------------------------------------------------------------------------------- |
| `REGISTRY`   | `ghcr.io`                                | Registry hostname.                                                               |
| `IMAGE_REPO` | `$GITHUB_REPOSITORY` (required if unset) | `czechbol/routeros-mcp` — lowercased before push.                                |
| `VERSION`    | `$GITHUB_REF_NAME` (required if unset)   | Semver with or without `v` prefix.                                               |
| `PUSH`       | `1`                                      | Set `0` to skip the registry push (then `mage release` is just `mage tarballs`). |

It:

1. Pushes a single multi-arch image to `REGISTRY/IMAGE_REPO` for
   `linux/arm64`, `linux/arm/v7`, `linux/amd64` with provenance + SBOM
   attestations.
2. Emits the tag aliases `<version>`, `<major>.<minor>`, `<major>`,
   `latest`. Pre-release versions (anything containing `-` or `+`)
   only get the exact tag.
3. Then builds per-arch images locally and saves them to
   `dist/routeros-mcp-<version>-linux-<arch>.tar` for RouterOS
   `/container/add`.

`mage tarballs` is the no-push subset: same per-arch tarballs, no
registry contact. Use it for on-router testing.

Cutting a release:

```sh
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

The workflow uses the default `GITHUB_TOKEN` for GHCR + Release upload.
No extra secrets needed.

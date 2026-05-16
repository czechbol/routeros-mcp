# routeros-mcp user guide

> Audience: you're comfortable on a Linux shell and you've used MikroTik
> RouterOS before, but you don't need to be an expert in containers,
> firewalls, or Go.

`routeros-mcp` is a small server that lets an LLM (Claude, etc.) drive a MikroTik
router through the **Model Context Protocol** (MCP). It exposes the entire
RouterOS REST API as **generic tools** so the model can list, add,
modify, remove, execute and discover RouterOS endpoints at runtime.

The server runs as an ~14 MB container. It is designed to run on the
router itself (via the RouterOS `container` feature) so you don't need any
extra hardware, but it also runs anywhere else that can reach the router's
REST API.

---

## 1. What you'll set up

```
                                   ┌──────────────────────────────────┐
                                   │ MikroTik router (RouterOS 7.x)    │
                                   │                                  │
   Claude Code  ─── HTTPS/HTTP ───►│  routeros-mcp container (port 8080)   │
   (or any MCP client)             │   │                              │
                                   │   └──HTTP basic auth──► RouterOS │
                                   │                          REST    │
                                   └──────────────────────────────────┘
```

- **MCP endpoint** the LLM talks to: `POST /mcp` on the container.
- **RouterOS REST** routeros-mcp talks to: `http(s)://<router>/rest/...` with
  HTTP basic auth.
- **Auth between LLM and routeros-mcp**: bearer token in
  `Authorization: Bearer …`.

You don't have to run it on the router. The same container also runs on
your laptop, a NAS, or a VM — set `ROS_URL` to point at the router.

---

## 2. Prerequisites

### On your build machine

- Docker with `buildx` — only needed for cross-arch tarball builds
  (e.g. building an `arm64` image from an `amd64` laptop).
- Go 1.25+ — only required if you want to build without Docker.
- `mage` — `go install github.com/magefile/mage@latest`.

### On the router (for "deploy on router" path)

- RouterOS 7.4 or newer.
- The `container` extras package installed (it ships as a separate
  `.npk`; download the matching extras bundle from mikrotik.com, upload
  `container-7.X-<arch>.npk` to `/file`, reboot).
- Container mode enabled at the device level
  (`/system/device-mode/update container=yes` — requires a physical
  reset-button press to confirm).
- The router's `www` REST service enabled, with the container's subnet
  in its allow list. We'll set this up below.

### Anywhere

- An MCP client. This guide assumes Claude Code (`claude` CLI).

---

## 3. Get the container

Every tagged release is published to `ghcr.io/czechbol/routeros-mcp` as
a multi-arch image for `linux/arm64`, `linux/arm/v7`, and `linux/amd64`.
RouterOS 7.4+ `/container/add` accepts `remote-image=` and pulls
directly from any OCI registry. The full path goes in `remote-image=`
(host + repo + tag); GHCR works without a separate `registry-url`.

```routeros
/container/add remote-image=ghcr.io/czechbol/routeros-mcp:latest \
  interface=veth-mcp envlist=mcp-env start-on-boot=yes
```

For routers without internet access, releases also attach per-arch OCI
tarballs as `routeros-mcp-<version>-linux-<arch>.tar`. Download with
`gh release download <version> --pattern "routeros-mcp-*-linux-arm64.tar"
--repo czechbol/routeros-mcp` or build with `mage tarballs`, then see
§8.6.

Pick the tarball matching your router's CPU architecture
(`/system/resource/print` shows it): `arm64` for most current ARM
boards, `arm/v7` for older 32-bit ARM, `amd64` for CHR and x86.

The stripped binary is ~14 MB and includes the embedded RouterOS 7.22.3
OpenAPI catalogue used by `ros_describe`.

---

## 4. Configuration (environment variables)

routeros-mcp is configured entirely through env vars. Defaults make local
development easy; production deploys should set bearer auth.

| Var                     | Default                        | Meaning                                                                                                                                                               |
| ----------------------- | ------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ROS_URL`               | `https://127.0.0.1`            | Base URL of the RouterOS REST API. Use HTTP if the router's `www` service is on and you'd rather skip TLS.                                                            |
| `ROS_USER`              | _(required)_                   | RouterOS username routeros-mcp logs in as. Use a dedicated user.                                                                                                      |
| `ROS_PASS`              | _(required)_                   | Password for `ROS_USER`.                                                                                                                                              |
| `ROS_INSECURE`          | `0`                            | Set `1` to skip TLS certificate verification (RouterOS ships self-signed certs by default).                                                                           |
| `MCP_TOKEN`             | _(required)_                   | Bearer token clients must send. Generate with `openssl rand -hex 32`.                                                                                                 |
| `MCP_ALLOW_ANON`        | `0`                            | Set `1` to disable bearer-token auth. Server still listens, but anybody who can reach `/mcp` controls the router — do not do this on a network you don't fully trust. |
| `LISTEN_ADDR`           | `0.0.0.0:8080`                 | HTTP listen address inside the container.                                                                                                                             |
| `ALLOWED_ORIGINS`       | _(empty = allow all)_          | Comma-separated list of allowed `Origin` request headers. Browser-only protection; useful when terminating TLS in front and worried about DNS-rebinding.              |
| `REDACT`                | `1`                            | Secret redaction. See §6.                                                                                                                                             |
| `REDACT_EXTRA`          | _(empty)_                      | Comma-separated extra field names to mask.                                                                                                                            |
| `DYNAMIC_OPENAPI`       | `1`                            | Live OpenAPI fetch. See §7.                                                                                                                                           |
| `OPENAPI_CACHE_DIR`     | `$XDG_CACHE_HOME/routeros-mcp` | Where successfully fetched specs are cached.                                                                                                                          |
| `OPENAPI_FETCH_TIMEOUT` | `10s`                          | HTTP timeout for the live-spec fetch.                                                                                                                                 |

> **Tip.** Generate a token once and keep it in your password manager:
> `openssl rand -hex 32`.

---

## 5. Run locally

If you just want to point routeros-mcp at a router from your laptop:

```sh
MCP_TOKEN=$(openssl rand -hex 32) \
ROS_URL=https://10.0.0.1 \
ROS_USER=admin ROS_PASS='…' \
ROS_INSECURE=1 \
  ./dist/routeros-mcp
```

Then point Claude Code at it:

```sh
claude mcp add --transport http ros http://localhost:8080/mcp \
  --header "Authorization: Bearer $MCP_TOKEN"
```

Verify in Claude with `/mcp` → "ros" → it should show **7 tools**.

---

## 6. Secret redaction

By default routeros-mcp walks every JSON response coming back from the router
and replaces values of known sensitive field names with `[REDACTED]`
before passing them to the LLM. This prevents passwords, WireGuard
private keys, SNMP communities, etc. from ever entering the model's
context.

Built-in field name list (case-insensitive):

```
password, passwd, secret, pre-shared-key, psk, private-key,
private-key-passphrase, community, snmp-community, api-key, token,
otp, recovery-passphrase, radius-secret, shared-secret,
wpa-pre-shared-key, wpa2-pre-shared-key
```

Extend with `REDACT_EXTRA=foo,bar` or disable wholesale with `REDACT=0`.

> Redaction is a defence-in-depth, not a guarantee. RouterOS field names
> change between versions; new sensitive fields may not be in the list.
> If you spot one, open a PR or set `REDACT_EXTRA`.

---

## 7. Dynamic OpenAPI

routeros-mcp ships with the RouterOS 7.22.3 OpenAPI catalogue embedded so the
`ros_describe` tool always works offline. At startup it also tries to
fetch the spec that matches **your** router's version from
[tikoci.github.io/restraml](https://tikoci.github.io/restraml/) so
`ros_describe` is accurate for your firmware.

The flow:

1. After the HTTP server is listening, a background goroutine queries
   `/rest/system/resource` on the router to discover its version.
2. It looks for a cached spec at `$OPENAPI_CACHE_DIR/openapi-<version>.json`.
3. If not cached, it fetches `tikoci.github.io/restraml/<version>/openapi.json`.
4. On success, it caches the response and installs it in-process.
5. On any failure (no network, no published spec for that version, 404,
   bad JSON), the embedded 7.22.3 catalogue stays active.

Restarts re-use the cached file, so this is a one-time network event.
Set `DYNAMIC_OPENAPI=0` to skip live fetching entirely.

> Currently only 7.22.x specs are published upstream. Routers on older
> RouterOS will silently fall back to the embedded 7.22.3 catalogue,
> which usually still matches most field names.

---

## 8. Deploy on the router

The container runs on the router itself, keeps a private connection to
RouterOS over `127.0.0.1`-equivalent networking, and is reachable from
the LAN through one NAT rule.

Applies to any RouterOS 7.4+ device with the `container` package and
device-mode enabled. RouterOS pulls the multi-arch image and picks the
matching architecture automatically — no per-device steps below.

### 8.1 Enable the container subsystem

Required once per router:

```routeros
# Confirm the package is installed
/system/package/print where name~"container"

# Enable container mode at the device level — physical reset/mode button
# press required to confirm; the router will tell you the deadline.
/system/device-mode/update container=yes
```

Reboot or press the button to confirm. Verify:

```routeros
/system/device-mode/print
# look for: container: yes
```

### 8.2 Create a bridge + veth for the container

The container will sit on its own `172.17.0.0/24` segment, isolated from
the LAN. We give the router `172.17.0.1` on that segment and the
container `172.17.0.2`.

```routeros
/interface/veth/add name=veth-mcp address=172.17.0.2/24 gateway=172.17.0.1
/interface/bridge/add name=br-docker
/ip/address/add address=172.17.0.1/24 interface=br-docker
/interface/bridge/port/add bridge=br-docker interface=veth-mcp
```

### 8.3 Allow the container to reach the RouterOS REST API

The router's REST API runs as the `www` service. Add the container
subnet to the allow list:

```routeros
/ip/service/print where name~"www"

# Append 172.17.0.0/24 to the existing address list:
/ip/service/set www address=<existing-csv>,172.17.0.0/24
```

If `www` is currently disabled and you'd rather use HTTPS, enable
`www-ssl` instead and set `ROS_URL=https://172.17.0.1` later. HTTP over
the local bridge is fine because the traffic never leaves the device.

### 8.4 Set the container's env vars

```routeros
/container/envs/add list=mcp-env key=ROS_URL value=http://172.17.0.1
/container/envs/add list=mcp-env key=ROS_USER value=<dedicated-user>
/container/envs/add list=mcp-env key=ROS_PASS value=<password>
/container/envs/add list=mcp-env key=MCP_TOKEN value=<paste your token>
# Optional, if using HTTPS with self-signed certs:
/container/envs/add list=mcp-env key=ROS_INSECURE value=1
```

### 8.5 Add forward + DNS rules so the container can reach the internet

This is **only** needed if you want `DYNAMIC_OPENAPI=1` (the default) to
fetch the live spec. If you set `DYNAMIC_OPENAPI=0`, skip this section.

```routeros
# Allow the container to forward traffic out the WAN
/ip/firewall/filter/add chain=forward action=accept \
  in-interface=br-docker out-interface=wan \
  comment="container -> internet" \
  place-before=[find where chain=forward and action=drop]

# srcnat masquerade on the WAN interface is already in the defconf
# ruleset on most routers; verify with:
/ip/firewall/nat/print where chain=srcnat
```

You also want the container to resolve DNS via the router rather than
hard-coded `8.8.8.8`, so it can use whichever resolver the router
already trusts:

```routeros
/ip/dns/print
# Make sure allow-remote-requests=yes; set if not:
/ip/dns/set allow-remote-requests=yes
```

### 8.6 Create the container

§8.5 (forward rule + DNS) must be in place so the router can reach the
registry.

```routeros
/container/add remote-image=ghcr.io/czechbol/routeros-mcp:latest \
  interface=veth-mcp envlist=mcp-env \
  dns=172.17.0.1,8.8.8.8 logging=yes start-on-boot=yes
# Watch the status until it shows STOPPED (pull + extraction done)
/container/print
/container/start 0
```

Pin a specific version (`:1.2.3`) instead of `:latest` for
reproducibility.

For routers without internet, `scp` a tarball from §3 and substitute
`file=routeros-mcp.tar` for `remote-image=…`:

```sh
scp dist/routeros-mcp-<version>-linux-<arch>.tar admin@<router-ip>:routeros-mcp.tar
```

```routeros
/container/add file=routeros-mcp.tar interface=veth-mcp envlist=mcp-env \
  dns=172.17.0.1,8.8.8.8 logging=yes start-on-boot=yes
/container/start 0
```

### 8.7 Expose the MCP port to the LAN

```routeros
/ip/firewall/nat/add chain=dstnat dst-port=8080 protocol=tcp \
  in-interface=bridge action=dst-nat to-addresses=172.17.0.2 to-ports=8080

/ip/firewall/filter/add chain=forward action=accept \
  in-interface=bridge out-interface=br-docker dst-port=8080 protocol=tcp \
  place-before=[find where chain=forward and action=drop]
```

Test:

```sh
curl http://<router-ip>:8080/healthz    # expect: ok
```

### 8.8 Wire up Claude Code

```sh
claude mcp add --transport http ros http://<router-ip>:8080/mcp \
  --header "Authorization: Bearer <your token>"
```

Open Claude, run `/mcp`, select "ros", confirm 7 tools are visible.

---

## 9. Available tools

| Tool             | RouterOS analogue                                                | Use it for                                                                                                                                                                                                                    |
| ---------------- | ---------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ros_print`      | `/path/print`                                                    | Listing items at any menu. Supports a `fields` array → server-side `.proplist`, dramatic context savings. Supports `limit` + `offset`.                                                                                        |
| `ros_add`        | `add`                                                            | Creating new items. Pass `path` and a `body` property map.                                                                                                                                                                    |
| `ros_set`        | `set`                                                            | Updating items by their RouterOS `.id` (`*1`, `*A`, …).                                                                                                                                                                       |
| `ros_remove`     | `remove`                                                         | Deleting items by `.id`.                                                                                                                                                                                                      |
| `ros_exec`       | actions (`ping`, `monitor`, `system/reboot`, `scheduler/run`, …) | Anything that isn't add/set/remove. Destructive paths (`reboot`, `shutdown`, `reset-configuration`, `factory-reset`) require `acknowledged_destructive=true`.                                                                 |
| `ros_list_paths` | —                                                                | Discover paths in the catalogue. `match=<substring>` filters; empty `match` returns top-level menus only.                                                                                                                     |
| `ros_describe`   | —                                                                | Look up the OpenAPI definition of a path. Returns each HTTP method's parameters (name, type, enum, default, required, description). Use this **before** add/set to learn the right field names for the live RouterOS version. |

Every list tool returns `format: "json"` or `format: "markdown"`. Default
is markdown (denser per token, easier for the model to scan). Set
`format=json` when you need to programmatically post-process.

---

## 10. Troubleshooting

### `connect: no route to host` to `172.17.0.1:80` in container log

The container started before the bridge or veth was ready. Newer
versions of routeros-mcp retry the version probe automatically; if your
container still misses, restart it:

```routeros
/container/stop 0 ; /container/start 0
```

### `dial tcp: lookup tikoci.github.io ...: i/o timeout`

The container can't resolve DNS. Either:

1. Configure DNS at container-create time:
   `/container/add … dns=172.17.0.1,8.8.8.8 …`, or
2. Set `DYNAMIC_OPENAPI=0` to skip the upstream fetch entirely (the
   embedded spec still works).

### `context deadline exceeded` fetching the OpenAPI spec

The container can resolve DNS but can't actually leave the router. Add
the forward rule from §8.5 and confirm with:

```routeros
/ip/firewall/filter/print where chain=forward
/ip/firewall/nat/print where chain=srcnat
```

### `openapi source: embedded 7.22.3 (no upstream spec published for this RouterOS version)`

The router is on an older version (e.g. 7.20.x). Upstream
`tikoci.github.io/restraml` only hosts the latest minor (currently
7.22.x). The embedded catalogue still works; nothing to do.

### Container "exited with status 1" on first start

Almost always missing or wrong env vars. RouterOS env-list assignments
sometimes don't propagate to a freshly-created container.

```routeros
/container/envs/print where list=mcp-env   # verify all 4 keys present
/container/stop 0 ; /container/remove 0
/container/add remote-image=ghcr.io/czechbol/routeros-mcp:latest \
  interface=veth-mcp envlist=mcp-env …
/container/start 0
```

### `/mcp` works in `curl` but Claude Code says "tools fetch failed"

Means the server is reachable but the tool schemas were rejected by the
client. Usually a stale build — pin a specific image tag and re-pull,
or rebuild and re-import the tarball.

### Storage too small for the image

On routers with limited internal flash (often ≤128 MB) you may need to
mount a USB stick for container storage and point `/container/config`'s
`layerdir` and `tmpdir` at it. See the MikroTik container documentation.

---

## 11. Security notes

- **Always set `MCP_TOKEN`.** Without it, anybody who can reach the
  MCP port can run any RouterOS command. Bearer auth is a hard guard,
  not advisory.
- The container has full REST access via its credentials. Use a
  **dedicated RouterOS user** with the minimum group rights you need
  (don't reuse `admin`).
- Bind the MCP port to a trusted interface only (default
  `0.0.0.0:8080`; the dst-nat rule restricts this to `in-interface=bridge`
  if you scope it).
- Terminate TLS in front (reverse proxy with a real certificate) before
  exposing `/mcp` to anything more than your LAN. Bearer over HTTP is
  fine on a trusted segment, not on the open internet.
- Redaction is on by default. Keep it on.

---

## 12. Updating

```routeros
/container/stop 0 ; /container/remove 0
/container/add remote-image=ghcr.io/czechbol/routeros-mcp:<version> \
  interface=veth-mcp envlist=mcp-env \
  dns=172.17.0.1,8.8.8.8 logging=yes start-on-boot=yes
/container/start 0
```

Env vars persist on the env list; re-entering them isn't needed unless
they change.

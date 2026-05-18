# RouterOS MCP

MCP server that exposes the MikroTik RouterOS REST API to LLMs as seven
generic tools. Designed to run as a ~14 MB scratch container — including
on the router itself via the RouterOS `container` feature.

## Tools

| Tool             | Purpose                                                                          |
| ---------------- | -------------------------------------------------------------------------------- |
| `ros_print`      | List items at any RouterOS menu path. `fields` triggers server-side `.proplist`. |
| `ros_add`        | Create items (`PUT /rest/<path>`).                                               |
| `ros_set`        | Update items by `.id` (`PATCH`).                                                 |
| `ros_remove`     | Delete items by `.id` (`DELETE`).                                                |
| `ros_exec`       | Arbitrary actions: ping, monitor, reboot, scheduler/run, etc.                    |
| `ros_list_paths` | Search the path catalogue (3635 paths).                                          |
| `ros_describe`   | OpenAPI lookup — operations, params, types, enums, defaults.                     |

## Quick start

### Off the router

Pull the image and point it at the router's REST API:

```sh
docker pull ghcr.io/czechbol/routeros-mcp:latest

MCP_TOKEN=$(openssl rand -hex 32) \
ROS_URL=https://10.0.0.1 ROS_USER=routeros-mcp ROS_PASS=… ROS_INSECURE=1 \
  docker run --rm -p 8080:8080 \
    -e MCP_TOKEN -e ROS_URL -e ROS_USER -e ROS_PASS -e ROS_INSECURE \
    ghcr.io/czechbol/routeros-mcp:latest
```

Wire up Claude Code:

```sh
claude mcp add --transport http ros http://localhost:8080/mcp \
  --header "Authorization: Bearer $MCP_TOKEN"
```

### On the router

Deploying inside RouterOS' `container` feature involves multiple
prerequisites — enabling container mode, creating a bridge + veth,
populating an env list, allowing the container subnet on the REST
service, and adding NAT/firewall rules to expose the MCP port.

Walk-through: [docs/user-guide.md §8](docs/user-guide.md#8-deploy-on-the-router).

## Contributing

Senior-eng-level notes — layout, mage targets, lint policy, tool
patterns, gotchas: see [docs/contributing.md](docs/contributing.md).

## Resource footprint

- Binary: ~14 MB stripped (includes 5.8 MB embedded RouterOS 7.22.3 OpenAPI shards)
- Image: ~14 MB (scratch + binary + CA bundle)
- RAM: ~30-50 MB resident

## Security

- Bearer-token auth on `/mcp` is required by default; opt out with
  `MCP_ALLOW_ANON=1` only on a trusted segment. `MCP_TOKEN` must be at
  least 32 characters — the server refuses to start otherwise. Generate
  one with `openssl rand -hex 32`. There is no per-IP rate limit on
  failed auth, so a high-entropy token is the only barrier against
  online brute force.
- Destructive actions (`ros_exec`, `ros_remove`) carry MCP
  `DestructiveHint: true` and rely on the client to prompt the user for
  confirmation. The server does not gate calls server-side — a meaningful
  RouterOS denylist is impractical, and a narrow keyword check would
  give false confidence. Run with a bearer token and review what your
  client surfaces before approval.
- RouterOS responses are walked and known sensitive fields are masked
  before reaching the LLM. Toggle with `REDACT=0`; extend with
  `REDACT_EXTRA`.
- Use a dedicated RouterOS user; don't reuse `admin`.
- The listener speaks plain HTTP — the bearer token rides in clear.
  Bind to loopback (`LISTEN_ADDR=127.0.0.1:8080`) when the MCP client is
  colocated, or terminate TLS in front (Caddy/nginx) for any non-LAN
  exposure or any segment with untrusted L2 peers.
- `ALLOWED_ORIGINS` (comma-separated) restricts the `/mcp` handler to
  requests with a matching `Origin` header. When set, requests without an
  `Origin` are rejected — non-browser clients (curl, native MCP clients
  that don't emit `Origin`) must either be left unconstrained by leaving
  `ALLOWED_ORIGINS` empty or be gated at the network layer instead.
  Strongly recommended on any deployment that doesn't bind to loopback —
  with the allowlist empty there is no defense-in-depth against another
  process on the same host or LAN that learns the token.

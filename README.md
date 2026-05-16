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

Pull a published image:

```sh
docker pull ghcr.io/czechbol/routeros-mcp:latest
```

…or build tarballs locally:

```sh
go install github.com/magefile/mage@latest
git clone …/routeros-mcp && cd routeros-mcp
mage tarballs   # -> dist/routeros-mcp-<version>-linux-<arch>.tar
```

Run locally:

```sh
MCP_TOKEN=$(openssl rand -hex 32) \
ROS_URL=https://10.0.0.1 ROS_USER=routeros-mcp ROS_PASS=… ROS_INSECURE=1 \
  ./dist/routeros-mcp
```

Wire up Claude Code:

```sh
claude mcp add --transport http ros http://localhost:8080/mcp \
  --header "Authorization: Bearer $MCP_TOKEN"
```

For the full walk-through (including deploying on the router itself,
firewall rules, DNS, troubleshooting): see [docs/user-guide.md](docs/user-guide.md).

## Contributing

Senior-eng-level notes — layout, mage targets, lint policy, tool
patterns, gotchas: see [docs/contributing.md](docs/contributing.md).

## Resource footprint

- Binary: ~14 MB stripped (includes 5.8 MB embedded RouterOS 7.22.3 OpenAPI shards)
- Image: ~14 MB (scratch + binary + CA bundle)
- RAM: ~30-50 MB resident

## Security

- Bearer-token auth on `/mcp` is required by default; opt out with
  `MCP_ALLOW_ANON=1` only on a trusted segment.
- RouterOS responses are walked and known sensitive fields are masked
  before reaching the LLM. Toggle with `REDACT=0`; extend with
  `REDACT_EXTRA`.
- Use a dedicated RouterOS user; don't reuse `admin`.
- Terminate TLS in front for any non-LAN exposure.

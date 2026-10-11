# Codex Space Bunny Proxy

[中文说明](README.zh-CN.md)

An unofficial, model-specific compatibility fork of [azain47/zen-proxy](https://github.com/azain47/zen-proxy), based on v0.3.0. It connects Codex CLI to OpenCode Zen's `space-bunny-free` through the Responses API interface.

The Space Bunny adapter supplies compatible CLI metadata and **real executable tool adapters**. `bash` maps to Codex's `exec_command`; `read` becomes a safely encoded Python file-reading command, also executed by Codex. The proxy itself never runs these commands or reads project files. Codex retains its tool execution, sandbox, and approval flow.

## Requirements

- Go 1.22+ to build; Codex CLI installed.
- Python 3 on the machine where Codex executes commands (for the read adapter).
- Network access to the upstream endpoint.
- Tested on Linux with Codex CLI 0.162.1 on 2026-10-11. Other platforms/versions are not verified.

## Quick start

Build from source:

```sh
git clone https://github.com/jackwangfeng/codex-space-bunny-proxy.git
cd codex-space-bunny-proxy
go build -o zen-proxy ./cmd/zen-proxy
```

Start the proxy in one terminal:

```sh
ZEN_SPACE_BUNNY_COMPAT=1 ZEN_MODEL=space-bunny-free ./zen-proxy
```

Default listener: `127.0.0.1:8788`. In another terminal, install an isolated Codex profile and launch it:

```sh
sh scripts/install-codex-profile.sh
codex --profile space-bunny
```

If the port is occupied, set `ZEN_PORT` on the proxy and `ZEN_PROXY_CODEX_BASE_URL` on the profile installer to matching addresses. The installer writes `space-bunny.config.toml`; back up an existing profile of that name before running it. It does not replace your default `config.toml`.

### Optional outbound proxy

If your network requires an HTTP proxy:

```sh
ZEN_SPACE_BUNNY_COMPAT=1 \
ZEN_MODEL=space-bunny-free \
ZEN_SPACE_BUNNY_HTTP_PROXY=http://127.0.0.1:YOUR_PROXY_PORT \
./zen-proxy
```

This setting changes outbound transport only for compatible Space Bunny requests. It is not the proxy's listen port. When unset, the upstream client's usual environment proxy behavior applies.

## What changes

The adapter is enabled only when all conditions hold:

1. `ZEN_SPACE_BUNNY_COMPAT=1`.
2. The requested model is `space-bunny-free`.
3. Codex declares its actual `exec_command` function.

It translates tool declarations, selected tool choice, assistant tool history, and returned tool arguments. Client metadata uses uniquely generated session/request identifiers; no captured user account or session IDs are shipped. Streaming text and reasoning pass through. Tool arguments are buffered until their upstream call is complete so they can be translated reliably.

Other model requests keep the inherited upstream behavior. Without the flag, the new adapter is inactive. Requests without a Codex execution tool do not acquire dummy tools and may be rejected by the provider.

## Verification

```sh
go test -race ./...
go vet ./...
```

Tests cover real file reading with shell-sensitive filenames, split streaming tool arguments, history replay, explicit tool selection, client identifier shape, and model-specific outbound proxy isolation.

Live checks on two Linux hosts verified normal Codex startup, multi-turn file reading, code editing, and running a two-test fixture. The fixture had failing tests before the change and passed afterward. No genuine OpenCode worker was needed for those direct Codex checks.

## Scope and limitations

This is a non-official compatibility implementation, not the OpenCode application. Upstream access rules may change; current acceptance is not a guarantee of continued availability. Rate limits, account eligibility, retired models, and service outages remain upstream conditions. This project neither promises unlimited usage nor changes quota accounting.

Only Space Bunny compatibility is the maintained addition. Inherited support for other models is not newly verified. The default endpoint is loopback and has no inbound authentication; see [SECURITY.md](SECURITY.md) before changing bind addresses. Tool approvals are still handled by Codex.

## Attribution and license

Upstream zen-proxy: **azain47 and contributors**, MIT. Space Bunny compatibility changes: **jackwangfeng**, MIT. Preserve [LICENSE](LICENSE) when redistributing. The inherited Codex prompt retains its Apache-2.0 notice and license: [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

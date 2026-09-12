# mcp-gateway

One MCP endpoint fronting N upstream MCP servers. Clients connect once; the gateway
discovers and aggregates their tool catalogs, resolves namespaces, and routes calls.

Written in Go against [go-sdk](https://github.com/modelcontextprotocol/go-sdk)
`v1.7.0-pre.1`.

**Status: in progress.** Transport, registry, aggregation, namespacing, hot reload,
reconnection, and the routing eval set are built. Policy, caching, semantic routing,
observability, and the benchmark harness are not. See [Roadmap](#roadmap).

## What works today

```
$ ./gateway
upstream "fs":  14 tools, protocol 2025-11-25
upstream "gh":  44 tools, protocol 2026-07-28
upstream "mem":  9 tools, protocol 2025-11-25
gateway ready: 67 tools
```

Three upstreams, 67 aggregated tools, **two protocol revisions negotiated
simultaneously** behind a single endpoint. Verified end to end in Claude Desktop.

- **Protocol** — MCP over stdio, both directions. Server to clients, client to upstreams.
- **Registry** — concurrent connect, aggregated catalog, per-upstream capability and
  protocol tracking. One upstream failing degrades the catalog rather than killing it.
- **Namespacing** — prefix-by-default (`fs__read_text_file`), stripped on the way out.
  Startup validation on namespace uniqueness.
- **Hot reload** — `notifications/tools/list_changed` from an upstream triggers a
  re-list and catalog rebuild.
- **Reconnection** — one supervisor goroutine per upstream blocked on `Wait()`. On death,
  tools leave the catalog and redial begins with exponential backoff (500ms → 30s).
  Recovery restores the catalog with no client involvement.
- **Graceful shutdown** — SIGINT/SIGTERM cancel the serve context, supervisors wind down,
  child processes are reaped.

## Design decisions

Full reasoning, including rejected alternatives, is in [DECISIONS.md](DECISIONS.md).
The load-bearing ones:

**Protocol revision is per-edge, not global.** Each upstream negotiates independently of
the client-facing side. Not hypothetical — `gh` speaks `2026-07-28` while `fs` and `mem`
speak `2025-11-25`, today, in one process.

**Routes resolve per request, not at registration.** Handlers call `registry.Lookup(name)`
at call time rather than capturing a session at startup. This is what makes reconnection
transparent: a session swapped inside the registry is picked up by the next call with no
re-registration. Verified by killing an upstream mid-run and calling a tool through the
replacement session.

**The SDK is transport; the gateway owns the catalog.** `tools/list` and `tools/call` are
handled in `AddReceivingMiddleware` and answered from the registry. `AddTool` is never
called. One source of truth, and per-session tool views (for policy and semantic routing)
drop in without restructuring.

**Never hold the registry lock across an upstream call.** Lock, copy the route, unlock,
then call. A hung upstream must not block writers — and since Go's `RWMutex` blocks new
readers behind a waiting writer, one slow upstream would otherwise freeze every request.

**Startup degrades, it does not fail fast.** One bad config entry should cost you that
entry, not access to every healthy upstream.

## Findings

Things that broke, found by running real software rather than reading the spec.

**`server/discover` can kill an upstream that predates it.** The `2026-07-28` handshake
probe sent to a server built on an older Python SDK fails validation against all 24 known
request types, propagates out of the receive loop, and the process exits. The supervisor
redials; the replacement crashes identically. The filesystem server survives the same probe
by rejecting it cleanly. So probe-based negotiation is only safe if every peer handles
unknown methods gracefully — in the wild, they don't. The spec has a correct path for this
(`UnsupportedProtocolVersionData`, SEP-2575); servers that predate it can't use it, and
`go-sdk v1.7.0-pre.1` exposes no client-side version pin.

**Liveness is not responsiveness.** An upstream that starts but never speaks MCP blocked
startup indefinitely, with two healthy upstreams connected but unreachable. A dead peer
fails fast; a hung one fails silently and takes its neighbours with it. Fixed with a
per-upstream connect deadline.

**Backoff is dominated by connect timeout against a hanging peer.** Measured redial
spacing was 16–17s despite a 500ms starting backoff, because each attempt consumed the
full 10s deadline. Exponential backoff behaves completely differently against a refusing
peer than a hanging one.

**Advertised capability is a claim, not a guarantee.** The filesystem server advertises
`tools.listChanged: true` and has never sent one. There is no way to distinguish "will
notify" from "says it will and won't", so nothing in the gateway depends on being told.

**`readOnlyHint` is not a cacheability signal.** `list_directory` is correctly marked
read-only, and its result is invalidated by `write_file` in the same catalog. Safe to
replay ≠ answer stays true. Caching will use annotation-gating *plus* write-invalidation
*plus* a TTL ceiling, with out-of-band mutation documented as uncacheable.

**`log.Fatalf` after acquiring resources is a leak.** It calls `os.Exit`, skipping every
`defer`. Graceful shutdown was dead code for its first two test runs and looked fine.

## Eval set

`eval/tasks.jsonl` — 89 hand-written tasks covering 36 of the 67 tools, each labelled with
the correct tool. Built before the routing work so the measurement can't be fitted to the
implementation.

Structural choices that make the eventual number defensible:

- **Situational phrasing, not tool vocabulary.** "Are we affected by CVE-2026-1142?" not
  "search commit messages for CVE-2026-1142". Tasks that restate the tool name let keyword
  matching score 100%, which would make routed and unrouted indistinguishable.
- **Matched paraphrase pairs** (`paraphrase_of` field) — same label, tool-shaped vs
  situational phrasing — so phrasing sensitivity can be reported separately from
  selection accuracy.
- **A deliberate hard tail** — vague, multi-step requests with no clean answer, labelled
  with the tool you'd reach for first. Expected to score badly.
- **Not LLM-generated.** A set written by the same class of model being evaluated measures
  inter-model agreement, not tool-selection quality.

Distribution is weighted toward realistic usage rather than forced uniform coverage, so
recall@k must be reported over the evaluated subset, not the full catalog.

## Running it

```bash
go build -o gateway .
cp config.example.yaml config.yaml   # then edit
./gateway
```

`config.yaml` is gitignored. Credentials come from the environment — `exec.Command`
inherits the parent environment, so `GITHUB_PERSONAL_ACCESS_TOKEN` never appears in a file.

As a connector, point your MCP client at the absolute path of the built binary.

## Roadmap

| Phase | Status |
|---|---|
| stdio transport, single upstream | done |
| config-driven multi-upstream, degraded startup | done |
| registry, aggregation, namespacing | done |
| honest capability advertisement | done |
| middleware-owned catalog, hot reload | done |
| reconnection with backoff, graceful shutdown | done |
| routing eval set (89 tasks) | done |
| streamable HTTP transport | not started |
| API key scopes, per-tool policy, rate limiting | not started |
| annotation-aware caching | not started |
| semantic tool routing + recall@k sweep | not started |
| OpenTelemetry, audit log, metrics | not started |
| benchmark + chaos harness, both tables | not started |

### Known limitations

- stdio only; remote upstreams not yet supported
- no client-side protocol version pin (SDK limitation) — an upstream that dies on
  `server/discover` cannot currently be used
- clients are not notified when the catalog changes; `tools.listChanged` is advertised
  as `false`, which remains accurate
- `tools/list` pagination unimplemented — fine at 67 tools, wrong at scale
- an in-flight dial is not aborted by shutdown; it completes, then exits

## Scope

Deliberately excluded: OAuth passthrough, any UI beyond metrics, and a fourth upstream.
Three upstreams across three unrelated domains is enough to prove aggregation, collision
handling, and version skew.

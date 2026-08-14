# Decisions

## 2026-07-28 — readOnlyHint is not a cacheability signal
Observed: filesystem server marks list_directory readOnlyHint:true.
Its result is invalidated by write_file, in the same catalog.
Decision: cache = readOnlyHint AND write-invalidation AND TTL ceiling.
Rejected: caching on readOnlyHint alone — replayable != stable.
Known gap: out-of-band mutation (vim) is undetectable. Documented, not solved.

## 2026-07-28 — Advertised capabilities must be derived, not defaulted
Observed: gateway declared logging:{} and tools.listChanged:true.
Upstream declared neither logging nor any live change mechanism.
Decision: capabilities = computed union over connected upstreams.
Why: advertising a capability we can't service is a protocol lie
     clients will act on.

## 2026-07-28 — Protocol revision is per-edge, not global
Observed: filesystem server negotiates 2025-11-25. Spec 2026-07-28
finalized today; upstreams have not moved.
Decision: negotiate independently on each edge, translate between them.
Rejected: pin 2025-11-25 (stale on arrival); pin 2026-07-28 only
          (no upstream speaks it).
Why: version translation across a stable boundary is the gateway's
     reason to exist. Same shape as Treble vendor-interface skew.

## 2026-07-29 — Unbounded connect blocks the entire gateway
Observed: upstream that starts but never speaks MCP (sleep 300).
Connect succeeds, ListTools blocks forever, wg.Wait() never returns.
Two healthy upstreams connected but never served. Gateway never started.
Root cause: context.Background() with no deadline anywhere.
Decision: per-upstream connect deadline; expiry = DEGRADED, not fatal.
Note: liveness != responsiveness. A hung upstream is worse than a dead
one — dead fails fast, hung fails silently and takes healthy peers with it.
Verified: SDK does not retain the connect context; defer cancel() is safe.

## 2026-07-29 — Startup degrades, it does not fail fast
Decision: an upstream that fails to connect is logged and skipped.
The gateway serves whatever catalog it managed to build.
Rejected: fail fast if any upstream is down — defensible for a single
service, wrong for a gateway, where one bad config entry would remove
access to every healthy upstream.
Also: connect concurrently, not sequentially. Sequential makes
"aggregation time vs upstream count" a self-inflicted straight line;
concurrent makes it flat until the slowest upstream, which is the
finding worth publishing.

## 2026-07-30 — Capabilities are per-capability, not a blanket union
Pass-through capabilities (logging) require an upstream that serves them.
Gateway-implemented capabilities (tools.listChanged) require machinery on
our side and are independent of what upstreams advertise.
Decision: advertise tools:{} only. listChanged flips to true on the day
change propagation ships, not before.
Rejected: naive union over upstream capabilities — would advertise
listChanged we cannot honour.

## 2026-07-31 — Routes resolve per request, not at registration
Handlers call registry.Lookup(name) at call time instead of capturing
a session at startup.
Why: reconnect can swap the session inside the registry and in-flight
routing picks it up with no re-registration. Capturing at startup would
pin every handler to a session that reconnect is about to replace.
Rule: never hold the registry lock across an upstream call. Lock, copy
the route, unlock, then call. A hung upstream must not block writers,
and RWMutex blocks new readers behind a waiting writer — one slow
upstream would freeze the whole gateway.

## 2026-08-14 — SDK is transport; gateway owns the catalog
tools/list and tools/call are handled in AddReceivingMiddleware and
answered from the registry. AddTool is no longer called at all.
Rejected: keep AddTool for dispatch, override only tools/list — two
catalogs that can drift, and hot reload would need RemoveTools+AddTool
on every change.
Consequence: SDK emits tools/list_changed only when AddTool/RemoveTools
mutate the server. Owning the catalog means owning that notification too.
Enables: per-session tool views for policy (day 7) and semantic
routing (day 10) with no further restructuring.
Debt: pagination (PageSize/NextCursor) now unimplemented — fine at 28
tools, wrong at 60+. Cacheable fields serialize as ttlMs:0/cacheScope:"".

## 2026-08-14 — Background retry with backoff, not on-demand or polling
Rejected on-demand reconnect: a dead upstream's tools leave the catalog,
so nothing can call them, so nothing triggers the reconnect. Deadlock.
Rejected fixed-interval polling: hammers a permanently-dead upstream and
can't distinguish a 2s blip from a misconfiguration.
Chosen: one supervisor goroutine per upstream blocked on Wait(),
exponential backoff 500ms→30s on redial.
Binder analogue: linkToDeath plus re-registration on restart. Stale-handle
bug avoided structurally by per-request Lookup — verified by calling a tool
through a session created after the original died.

## 2026-08-14 — Backoff is dominated by connect timeout on hung upstreams
Observed: redial attempts 16-17s apart despite 500ms starting backoff.
The hung upstream consumes the full 10s connect timeout each attempt, so
real spacing is backoff + timeout.
Implication: exponential backoff behaves completely differently against a
refusing peer (fast fail, true exponential) vs a hanging one (timeout floor).
Chaos matrix row.

## 2026-08-14 — log.Fatalf after acquiring resources is a leak
Observed: Fatalf on Run's error called os.Exit, skipping every defer
including reg.Close(). Graceful shutdown was dead code for its first
two test runs.
Rule: Fatalf only before anything needing release exists. After that,
Printf and return.
Also: context.Canceled from Run on SIGINT is the requested outcome, not
a failure — errors.Is guard, consuming the %w chains built earlier.
Debt: an in-flight dial isn't aborted by shutdown; it completes then exits.

## 2026-08-14 — server/discover can kill an upstream that predates it
Observed: crystaldba/postgres-mcp (Python SDK, pre-2026-07-28) receives
server/discover, fails pydantic validation against all 24 known request
types, propagates out of the receive loop, and the process exits.
Supervisor redials, fresh container crashes identically, forever.
Filesystem server survives the same probe — rejects cleanly, Go SDK falls
back to initialize, negotiates 2025-11-25.
So: probe-based negotiation is only safe if every peer rejects unknown
methods gracefully. In the wild they don't.
The spec has a correct path for this — UnsupportedProtocolVersionData
(SEP-2575), an error listing supported versions. Old servers can't use it
because they predate it.
No client-side version pin exists in go-sdk v1.7.0-pre.1.
ProtocolVersionSupporter is server-side only (filters what Server.Connect
advertises), not a way to constrain what our client requests.
Decision: drop postgres upstream. Document as a known limitation needing
SDK support. Do NOT fork the transport — a day's work for one upstream.
Chaos matrix: "upstream killed by version probe, crash-loops under
supervision." Strongest row so far.
Also: Postgres reference server is archived anyway (Anthropic handed 13
reference servers to vendors after AAIF governance transfer, Dec 2025).

## 2026-08-14 — Eval set hand-written against the real 67-tool catalog
Tasks phrased as a user would ask, not restating tool names — otherwise
every approach scores 100% on string matching.
Deliberate confusable pairs: fs__search_files vs gh__search_code (#5/#6),
fs__edit_file vs fs__write_file (#19, where the wrong answer destroys
the file).
Dropped two drafted tasks with no correct answer in the catalog (CI status,
starred repos). A task no tool can serve poisons recall for every approach.
Not LLM-generated: a set written by the same class of model that will be
evaluated measures agreement between models, not tool-selection quality.
Known gap at 34 tasks: distribution is near one-task-per-tool, which tests
coverage rather than selection. Remaining tasks weight toward
high-frequency tools and add vague/multi-step cases.
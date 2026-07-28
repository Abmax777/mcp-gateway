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

# Follow Upstream Redirects When Caching

**Date:** 2026-05-05
**Status:** Approved, ready for implementation plan

## Problem

The proxy caches every 2xx–3xx upstream response under the cache key derived
from the original request URL. When a registry returns a `302 Found` (or any
other redirect) pointing at a signed/short-lived CDN URL, the proxy caches
the redirect itself — including its `Location` header.

Concrete failure shape for CI use cases:

1. Client requests `https://registry/foo.tar`.
2. Proxy forwards, upstream returns `302` with
   `Location: https://signed.s3.aws/...?signature=xyz&expires=600`.
3. Proxy caches the `302` under the key for `https://registry/foo.tar`.
4. Client follows the redirect, requests the signed URL through the proxy,
   which forwards and caches the body under the *signed URL's* key.
5. On the next CI run, the proxy serves the cached `302` for
   `https://registry/foo.tar`. Its `Location` URL has expired or is
   single-use → client follows a broken URL → build fails.
6. Even if the signed URL hadn't expired, the cached blob lives under the
   *signed* URL's key, which differs from run to run, so it's effectively
   never reused.

The cache is keyed on something stable (the original URL) but stores
something unstable (a redirect to a transient URL). The fix is to make
the proxy follow the redirect chain server-side and cache the *terminal*
body under the original URL's key.

## Goals

- The proxy follows upstream redirects and caches the final response body
  under the original request's cache key.
- The client receives the final response directly (status 200, final
  headers, no `Location` header).
- On subsequent requests for the same original URL, the cache hit returns
  the bytes — no signed/transient URLs are exposed to the client.
- Behavior is on by default, with no new flag.

## Non-Goals

- Preserving redirect responses anywhere in cache or in the response to
  the client.
- Caching intermediate hops in the redirect chain.
- Honoring `Cache-Control` directives on intermediate redirects.
- Special handling for `304 Not Modified` (it is not a redirect; today's
  behavior is preserved).
- Per-hop timeouts. The existing `UpstreamTimeout` covers the whole chain.

## Scope of redirect codes followed

All standard redirect status codes:

- `301 Moved Permanently`
- `302 Found`
- `303 See Other` (per stdlib, transforms `POST` body to `GET` on the next
  hop; not a concern in practice since the cache only handles `GET`/`HEAD`)
- `307 Temporary Redirect`
- `308 Permanent Redirect`

## Architecture

A new file `internal/proxy/transport.go` defines a `redirectFollower` type
that implements `http.RoundTripper`. It composes an `*http.Client` whose
default redirect policy follows up to 10 hops, using `net/http`'s standard
behavior for relative-URL resolution, cross-host header stripping
(`Authorization`, `Cookie`, `WWW-Authenticate`), and cycle detection (via
the hop limit).

`proxy.New()` sets `goproxy.ProxyHttpServer.Tr = &redirectFollower{...}`.
The handler in `internal/proxy/handler.go` is unchanged. `HandleRequest`
still computes the cache key from the original request before upstream is
called; `HandleResponse` still receives a single response (now the
*final* one after the chain) and caches it under the original key.

A short comment on `redirectFollower.RoundTrip` notes that this layer
intentionally exceeds the standard `RoundTripper` contract (a single round
trip with no redirects) and explains why: caching needs the terminal body,
and goproxy invokes us through this single seam.

## Data flow

```
Client → CONNECT/MITM → HandleRequest
  ├─ bypass? → forward unchanged (no redirect-following changes)
  ├─ cache hit → return cached final body (already terminal)
  └─ cache miss → goproxy calls Tr.RoundTrip
                    └─ redirectFollower.RoundTrip
                         ├─ http.Client.Do(req)
                         │     ├─ hop 1: GET origin/foo  → 302 Location: signed.cdn/...
                         │     ├─ hop 2: GET signed.cdn/... → 200 + body
                         │     └─ stops at 200 (or after 10 hops → error)
                         └─ returns final response
                  → HandleResponse writes final body to cache under
                    the ORIGINAL URL's key
                  → response forwarded to client
                    (status 200, final headers, no Location header)
```

Specifics that follow from this:

- The cache key is derived from `req.URL` in `HandleRequest` *before*
  upstream is called, so it always reflects the URL the client asked for.
- `ctx.Req.URL` (used by `HandleResponse` for logging and `EntryMeta.URL`)
  remains the original URL.
- The cached `EntryMeta.Header` is the *final* response's headers.
- When the chain ends in 4xx/5xx, the existing
  `if resp.StatusCode < 200 || resp.StatusCode >= 400` guard in
  `HandleResponse` skips caching. The client receives the error verbatim.

## Error handling

| Situation | Behavior |
|---|---|
| Chain exceeds 10 hops | `http.Client` returns "stopped after 10 redirects". `redirectFollower.RoundTrip` returns the error. goproxy renders it as 502 to the client. Nothing cached. |
| Redirect loop | Caught by the 10-hop limit. Same as above. |
| Redirect target unreachable / DNS failure / TLS error | `http.Client.Do` returns the underlying error. 502 to client. Nothing cached. |
| Final response is 4xx/5xx | Existing `HandleResponse` guard skips caching. Client gets the error verbatim. |
| Relative `Location` (`/v2/foo`) or scheme-relative (`//host/foo`) | Resolved by `http.Client` against the previous request's URL. |
| Malformed `Location` | Parse error from `http.Client.Do`. 502 to client, nothing cached. |
| Cross-host hop carrying `Authorization` / `Cookie` | Stripped by `http.Client` before the next hop (stdlib default). |
| `UpstreamTimeout` mid-chain | Applied as `http.Client.Timeout` so it covers the whole chain end-to-end. Timeout → 502, nothing cached. |
| Client cancels mid-chain | `req.Context()` is propagated to `client.Do`; cancellation aborts the chain. |
| `304 Not Modified` | Not a redirect — passes through unchanged. No behavior change. |

`http.Client.Do` reads and discards intermediate response bodies so
connections can be reused. The terminal body is the only one that reaches
`HandleResponse`.

## Testing

Tests live in `internal/proxy/proxy_test.go` alongside the existing
proxy-level integration tests, using the same `setupProxy` helper.

| Test | Verifies |
|---|---|
| `TestProxy_FollowsRedirectAndCachesFinalBody` | Upstream serves `/redirect` → 302 → `/final` (200 "blob"). First request gets `200 "blob"`. Second request is a cache hit (upstream `/final` and `/redirect` call counts stay at 1). Cache key is the original `/redirect` URL. |
| `TestProxy_FollowsMultiHopRedirect` | Two 302s in sequence, then 200. Final body returned and cached under the original URL. |
| `TestProxy_FollowsRedirectsAcrossHosts` | Two `httptest.Server`s; first redirects to second. Cross-host hop succeeds; cache key is the *original* host's URL. |
| `TestProxy_StripsAuthHeaderOnCrossHostRedirect` | Client sends `Authorization: Bearer x`. First server records the header, redirects to second host. Second server records its absence. Confirms stdlib header-stripping reaches us. |
| `TestProxy_TooManyRedirectsReturns502` | Server redirects to itself in a loop. Client gets 502; cache stays empty. |
| `TestProxy_RedirectChainEndingIn404NotCached` | 302 → 404. Two requests both hit upstream (4xx not cached, current behavior preserved). |
| `TestProxy_OfflineMode_ServesCachedFinalBody` | Populate cache in serve mode via a 302→200 chain. Switch to offline mode against the same on-disk cache. Request the original URL → served as 200 from cache, no upstream call (upstream shut down). End-to-end proof of the CI replay fix. |
| `TestProxy_RelativeLocationRedirect` | Upstream returns `Location: /elsewhere` (relative). Verifies stdlib resolves it correctly. |

A unit test on `redirectFollower` itself is not needed — its behavior is
`http.Client.Do`'s behavior, which the stdlib already tests. The
proxy-level tests above exercise the full path.

## Files Changed

- `internal/proxy/transport.go` — new file: `redirectFollower` type.
- `internal/proxy/proxy.go` — wire `proxy.Tr` to a `redirectFollower`
  configured with the `UpstreamTimeout` from `Config`.
- `internal/proxy/proxy_test.go` — new tests listed above.
- `README.md` — short note in the request-flow section that the proxy
  follows upstream redirects and caches the terminal body under the
  original URL.

No changes to the cache layer, storage layer, archive formats, CLI, or
config schema.

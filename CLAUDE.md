# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build, lint, test

- Module: `github.com/denysvitali/search-mcp`, Go 1.25.5.
- Build: `go build .` (or `go run .` to execute directly).
- Lint: `golangci-lint run` (CI pins `v2.11.4`, see `.golangci.yml`).
- Vet: `go vet ./...`.
- Unit tests: `go test ./...`.
- Single test: `go test ./internal/resilience/ -run TestRetry -race` (add `-v` for verbose).
- Race + coverage (matches CI): `go test -race -covermode=atomic -coverprofile=coverage.out ./...`.
- Integration tests: `go test -run Integration .` from repo root — they `go build` the binary, start an `httptest` mock, and exercise both the CLI (`search ... --json`) and the MCP stdio/HTTP servers (`serve`) via `github.com/modelcontextprotocol/go-sdk/mcp`. They set `SEARCH_MCP_PROVIDERS` (see `mockOnlyEnv`) to keep the run hermetic — without it the fan-out default would reach the live network.
- Release dry run: `goreleaser check` then `goreleaser build --snapshot --clean --single-target`.

## Architecture

Two entry points share one `search.Service`:

- **CLI** (`cmd/`, `cobra` + `viper`): `search QUERY`, `read URL`, `serve` (MCP stdio), `version`.
- **MCP server** (`cmd/serve.go`): exposes `search`, `search_batch`, `web_read`, `read_pdf`, and `provider_status`. Transport is stdio, or streamable HTTP with `--http <addr>`.

`main.go` just calls `cmd.Execute()`. The `version` var is overridden at build via `-ldflags "-X main.version=..."` (GoReleaser does this).

### Search pipeline

`search.Service` (`internal/search/service.go`) holds a `map[name]Provider` and a `map[name]*rate.Limiter` (one `golang.org/x/time/rate.Limiter` per provider, sized by `--rate-rps` / `--rate-burst`). It emits OTel spans + a `search_requests_total` counter and a `search_request_duration_ms` histogram (both gracefully nil if the meter rejects them).

**An empty `Request.Provider` means fan-out** (`AllProviders`, i.e. `"all"`), not "pick the first provider". `searchAll` (`merge.go`) queries every provider in parallel, merges with reciprocal rank fusion (`rrfK = 60`) deduplicating on `normalizeResultURL`, and reports the providers that failed in `Response.Degraded`. This is the default because every public backend fails independently and often.

When a provider *is* named, `Search` walks a **deterministic order** — the requested provider first, then remaining providers sorted by name — falling through on **any** error except caller cancellation (`isFallbackWorthy` in `service.go`). That deliberately includes `resilience.ErrCircuitOpen`, upstream 5xx, transport errors and parse failures: they are all per-provider conditions, and an earlier narrower rule meant an open breaker on the primary killed every search for the whole cooldown. Caller errors (empty query, unknown provider) return before the loop and stay terminal. When everything fails, `joinProviderErrors` wraps one `"<name>: <reason>"` per provider via `errors.Join`.

Context cancellation/deadline aborts the loop immediately and surfaces `ctx.Err()`, never masked by a provider error. The sentinels live in `internal/search/errors.go` (not `internal/provider`) to avoid the import cycle: `provider` imports `search`, and `provider/errors.go` re-exports them as aliases so `errors.Is` works either way.

### Resilience decorators

`internal/resilience.Wrap` stacks decorators **innermost-out as cache(breaker(retry(inner)))**:

- `RetryProvider`: exponential backoff with full jitter (`[d/2, d]`). Retries on `ErrRateLimited` and generic errors; **does not** retry on `ErrBlocked` or context errors. Test hooks: `RetryOptions.sleep` and `RetryOptions.jitter` replace the real timer/RNG.
- `CircuitBreaker`: per-provider `closed → open → half-open`. Single in-flight half-open trial; success closes, failure re-opens. Exposes `resilience.ErrCircuitOpen`.
- `CachingProvider`: in-memory TTL map keyed by a stable key over query/count/country/language/safesearch/freshness/provider. `MaxEntries=1024` hard cap with lazy-expire-then-reset eviction. `TTL<=0` is a pass-through.

Decorators implement `search.Provider` and forward `Name()` to the inner — important so the service's `ProviderNames()` map and the breaker key by the same identity.

### Providers (`internal/provider/`)

All share `common.NewHTTPClient` (15s timeout) and `common.LimitedBody` (`io.LimitReader` at 10 MiB). `common.ApplyExtraHeaders` overlays `request.ExtraHeaders` last so callers can override defaults.

**Soft-block detection (`common/block.go`) is load-bearing.** HTML backends answer a fingerprinted client with a valid 2xx page that has no results. Two checks turn that into `ErrBlocked`: `IsChallengePage` (captcha / anomaly / Cloudflare markers) and `ErrMissingResultsContainer`, used when the page parses but the results container is absent. The second matters as much as the first — a real zero-match query still renders its container, so a missing one means a challenge *or* upstream markup drift. Without it both look like a successful empty search: the breaker stays closed, `provider_status` reports healthy, and every future query pays a wasted round trip. Each provider's `testdata/` holds a real captured SERP plus its challenge page.

Providers that can paginate loop over `searchPage(ctx, req, page)` until they have `req.Count` results, deduplicating by URL and capped at 3 pages. A failure on page 2+ keeps whatever earlier pages returned; a failure on page 1 propagates so the service can fall back.

- `duckduckgo`: POSTs to `https://html.duckduckgo.com/html/`, parses `result__body` / `result__a` / `result__snippet` inside `div.results`, unwraps `/l/?uddg=…` redirects. Params: `kl` region, `df` freshness, `kp` safesearch. **Does not paginate** — the real next-page cursor is `s`+`vqd`+`nextParams` and that POST is bot-gated even from a warm IP, so it stays at one page (~10 results).
- `marginalia`: GETs `https://api.marginalia.nu/public/search/{query}` — the query is a **path segment**, so it is `url.PathEscape`d, not form-encoded. Keyless JSON, 20 results per call.
- `yahoo`: GETs `https://search.yahoo.com/search`, parses `div.algo-sr` inside `#web`, unwraps `/RU=…/RK=` tracking links. Pages via the 1-based `b` offset (`b=11`, `b=21`).
- `mojeek`: GETs `https://www.mojeek.com/search`, parses `ul.results-standard > li`. HTTP 429 → `ErrRateLimited`. Freshness mapped to `since=YYYYMMDD`. **Not in the default provider set** — it serves datacenter IPs an HTTP 200 captcha regardless of User-Agent.
- `brave`: JSON API at `https://api.search.brave.com/res/v1/web/search`, header `X-Subscription-Token`. `count` is clamped to `braveMaxCount` (20 — larger values are a 422); larger requests page via `offset` (0-9) and stop early on a short page, since each page is a billed call. Two constructors: `NewBrave` (back-compat, records key error) and `NewBraveChecked` (returns the error). Only registered when `--brave-api-key` is set.
- `searxng`: JSON API at `<base>/search?format=json`, pages via `pageno`.

`search.SplitPublished` (`internal/search/dateparse.go`) lifts the `"Jul 16, 2025 · "`-style date prefix that Yahoo and DuckDuckGo bury in the snippet into `Result.Published`, normalised to `YYYY-MM-DD`.

### Reader (`internal/reader/`)

`Read(ctx, urlStr)` validates scheme (`http`/`https`) then dispatches URL-shape predicates to a site-specific fetcher, falling back to `fetchGenericHTMLAsMarkdown` (HTML → `html-to-markdown` v2, then `cleanMarkdown` to collapse blank lines and trim).

Site-specific readers: `github.go` (repos + issues/PRs via the GitHub API), `reddit.go` (`.json` thread append), `hackernews.go` (Algolia + Firebase), `stackoverflow.go` (API), `arxiv.go` (abs page), `gerrit.go` (any Gerrit instance's `/c/<project>/+/<change>` via its REST API — strips the `)]}'` XSSI prefix, falls back from `<project>~<id>` to the bare change id), `gitiles.go` (any Gitiles instance's `/<project>/+/<ref>/<path>`: `?format=TEXT` blobs — whose ls-tree output for trees is parsed as a directory listing — then `?format=JSON` trees, then scraped HTML; falls through to the generic fetch if all fail). Each file owns the URL-shape predicate and the renderer; `reader.go` only wires the dispatch.

**SSRF guard**: `guardDialAddress` runs in the dialer's `Control` callback (post-DNS, pre-connect) and rejects loopback, private, link-local, unspecified, and multicast IPs — `isDisallowedIP` covers both v4 and v4-in-v6 forms. `allowPrivateHosts` is a test-only escape hatch flipped by the test suite to hit `httptest` loopback servers. Response bodies are capped at 10 MiB (`maxResponseBodyBytes`); error bodies at 4 KiB (`readErrorBody`).

### Observability (`internal/observability/`)

`Setup` returns a shutdown closure; merged via `resource.Merge` with `resource.NewSchemaless` so the SDK's default schema URL never conflicts. Supports `stdout` (writes to `os.Stderr` from the CLI) and `otlp` exporters. Standard `OTEL_EXPORTER_OTLP_ENDPOINT` is honored by the exporter packages; `--otel-endpoint` overrides.

### Config

Priority: flags > `SEARCH_MCP_*` env > `$HOME/.config/search-mcp/search-mcp.yaml`. Config search intentionally **does not** look in `.` so another project's `search-mcp.yaml` can't shadow the user's — use `--config <path>` to point at a project-local file. All keys are listed in `cmd/root.go` (both flag + `viper.BindPFlag` lines).

`--providers` selects the keyless provider set (default `duckduckgo,marginalia,yahoo` — see `defaultKeylessProviders`); keyed providers are still enabled by the presence of their API key. `keylessProviderSet` splits entries on commas because viper hands a `SEARCH_MCP_PROVIDERS` env var through as one string, so `"a,b"` would otherwise arrive as a single unmatchable name.

## Conventions

- Errors are wrapped with `fmt.Errorf("...: %w", sentinel)` so `errors.Is` classifies them. Don't shadow `search.ErrRateLimited` / `search.ErrBlocked`.
- Provider implementations must implement `search.Provider` (Name + Search) and use `newHTTPClient` + `limitedBody` + `applyExtraHeaders`.
- All HTTP I/O uses `context.Context`; the search service aborts on `ctx.Err()` and surfaces it directly.
- Tests sit alongside the package they cover (`*_test.go`). Integration lives at the repo root and builds a real binary — do not move it under `internal/`.
- Commit messages are conventional commits (`feat:`, `fix:`, `refactor:`, `test:`, `ci:`); keep the body focused on the *why*.

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
- Integration tests: `go test -run Integration .` from repo root — they `go build` the binary, start an `httptest` mock, and exercise both the CLI (`search ... --json`) and the MCP stdio server (`serve`) via `mark3labs/mcp-go/client`.
- Release dry run: `goreleaser check` then `goreleaser build --snapshot --clean --single-target`.

## Architecture

Two entry points share one `search.Service`:

- **CLI** (`cmd/`, `cobra` + `viper`): `search QUERY`, `read URL`, `serve` (MCP stdio), `version`.
- **MCP server** (`cmd/serve.go`): exposes `search` and `web_read` tools, transport is stdio.

`main.go` just calls `cmd.Execute()`. The `version` var is overridden at build via `-ldflags "-X main.version=..."` (GoReleaser does this).

### Search pipeline

`search.Service` (`internal/search/service.go`) holds a `map[name]Provider` and a `map[name]*rate.Limiter` (one `golang.org/x/time/rate.Limiter` per provider, sized by `--rate-rps` / `--rate-burst`). It emits OTel spans + a `search_requests_total` counter and a `search_request_duration_ms` histogram (both gracefully nil if the meter rejects them).

`Search` walks providers in a **deterministic order** — the requested provider first, then remaining providers sorted by name — and only falls through to the next one when the current call returns a *fallback-worthy* error: `search.ErrRateLimited` or `search.ErrBlocked`. Context cancellation/deadline aborts the loop immediately and surfaces `ctx.Err()`, never masked by a provider error. These sentinels live in `internal/search/errors.go` (not `internal/provider`) to avoid the import cycle: `provider` imports `search`, and `provider/errors.go` re-exports them as aliases so `errors.Is` works either way.

### Resilience decorators

`internal/resilience.Wrap` stacks decorators **innermost-out as cache(breaker(retry(inner)))**:

- `RetryProvider`: exponential backoff with full jitter (`[d/2, d]`). Retries on `ErrRateLimited` and generic errors; **does not** retry on `ErrBlocked` or context errors. Test hooks: `RetryOptions.sleep` and `RetryOptions.jitter` replace the real timer/RNG.
- `CircuitBreaker`: per-provider `closed → open → half-open`. Single in-flight half-open trial; success closes, failure re-opens. Exposes `resilience.ErrCircuitOpen`.
- `CachingProvider`: in-memory TTL map keyed by a stable key over query/count/country/language/safesearch/freshness/provider. `MaxEntries=1024` hard cap with lazy-expire-then-reset eviction. `TTL<=0` is a pass-through.

Decorators implement `search.Provider` and forward `Name()` to the inner — important so the service's `ProviderNames()` map and the breaker key by the same identity.

### Providers (`internal/provider/`)

All share `newHTTPClient` (15s timeout) and `limitedBody` (`io.LimitReader` at 10 MiB). `applyExtraHeaders` overlays `request.ExtraHeaders` last so callers can override defaults.

- `duckduckgo`: POSTs to `https://html.duckduckgo.com/html/`, parses `result__body` / `result__a` / `result__snippet` via `htmlutil`, unwraps `/l/?uddg=…` redirects. Detects DDG's anomaly/captcha page (200 with `anomaly.js` *or* 202) and returns `ErrBlocked` so the service falls back.
- `mojeek`: GETs `https://www.mojeek.com/search`, parses `ul.results-standard > li`. HTTP 429 → `ErrRateLimited`. Freshness mapped to `since=YYYYMMDD`.
- `brave`: JSON API at `https://api.search.brave.com/res/v1/web/search`, header `X-Subscription-Token`. Two constructors: `NewBrave` (back-compat, records key error) and `NewBraveChecked` (returns the error). Provider is only registered when `--brave-api-key` is set.

### Reader (`internal/reader/`)

`Read(ctx, urlStr)` validates scheme (`http`/`https`) then dispatches URL-shape predicates to a site-specific fetcher, falling back to `fetchGenericHTMLAsMarkdown` (HTML → `html-to-markdown` v2, then `cleanMarkdown` to collapse blank lines and trim).

Site-specific readers: `github.go` (repos + issues/PRs via the GitHub API), `reddit.go` (`.json` thread append), `hackernews.go` (Algolia + Firebase), `stackoverflow.go` (API), `arxiv.go` (abs page). Each file owns the URL-shape predicate and the renderer; `reader.go` only wires the dispatch.

**SSRF guard**: `guardDialAddress` runs in the dialer's `Control` callback (post-DNS, pre-connect) and rejects loopback, private, link-local, unspecified, and multicast IPs — `isDisallowedIP` covers both v4 and v4-in-v6 forms. `allowPrivateHosts` is a test-only escape hatch flipped by the test suite to hit `httptest` loopback servers. Response bodies are capped at 10 MiB (`maxResponseBodyBytes`); error bodies at 4 KiB (`readErrorBody`).

### Observability (`internal/observability/`)

`Setup` returns a shutdown closure; merged via `resource.Merge` with `resource.NewSchemaless` so the SDK's default schema URL never conflicts. Supports `stdout` (writes to `os.Stderr` from the CLI) and `otlp` exporters. Standard `OTEL_EXPORTER_OTLP_ENDPOINT` is honored by the exporter packages; `--otel-endpoint` overrides.

### Config

Priority: flags > `SEARCH_MCP_*` env > `$HOME/.config/search-mcp/search-mcp.yaml`. Config search intentionally **does not** look in `.` so another project's `search-mcp.yaml` can't shadow the user's — use `--config <path>` to point at a project-local file. All keys are listed in `cmd/root.go` (both flag + `viper.BindPFlag` lines).

## Conventions

- Errors are wrapped with `fmt.Errorf("...: %w", sentinel)` so `errors.Is` classifies them. Don't shadow `search.ErrRateLimited` / `search.ErrBlocked`.
- Provider implementations must implement `search.Provider` (Name + Search) and use `newHTTPClient` + `limitedBody` + `applyExtraHeaders`.
- All HTTP I/O uses `context.Context`; the search service aborts on `ctx.Err()` and surfaces it directly.
- Tests sit alongside the package they cover (`*_test.go`). Integration lives at the repo root and builds a real binary — do not move it under `internal/`.
- Commit messages are conventional commits (`feat:`, `fix:`, `refactor:`, `test:`, `ci:`); keep the body focused on the *why*.

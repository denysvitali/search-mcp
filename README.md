# search-mcp

Go MCP server and CLI for web search.

Provider implementations live in dedicated packages under `internal/provider/`
(`duckduckgo`, `marginalia`, `mojeek`, `yahoo`, `brave`, `searxng`, `kagi`,
`exa`, and `tavily`). Each package implements `search.Provider` and registers
its constructor from `init()`; the command imports the packages for
registration and builds only the providers enabled by configuration.

## Providers

### Keyless (enabled by default)

By default `duckduckgo`, `marginalia`, and `yahoo` are enabled. Change the set
with `--providers` (or `SEARCH_MCP_PROVIDERS`), e.g.
`--providers duckduckgo,yahoo`.

- `duckduckgo`: scrapes `https://html.duckduckgo.com/html/` (the same endpoint the DuckDuckGo web UI uses). DDG aggressively rate-limits datacenter IPs and serves an anomaly/captcha page after a few requests; the provider detects this and reports it as blocked so the search falls back. Its HTML endpoint returns about ten results per page and its next-page cursor is bot-gated, so it does not paginate.
- `marginalia`: uses `https://api.marginalia.nu/public/search`, a documented JSON API with no key and no bot wall. Marginalia is an independent, non-commercial crawler favouring small, text-heavy, non-SEO-optimised sites — weaker on mainstream queries, but a dependable floor when the HTML scrapers are being challenged, and it returns twenty results in one call.
- `yahoo`: scrapes Yahoo's public HTML results. Tracking links are unwrapped to their destination URLs, snippet date prefixes are lifted into `published`, and it pages via the `b` offset (up to three pages) to satisfy larger `count` values.
- `mojeek`: scrapes `https://www.mojeek.com/search`. **Not enabled by default** — Mojeek currently answers datacenter IPs with an HTTP 200 captcha page regardless of User-Agent, so it costs a round trip while returning nothing. Re-enable it with `--providers duckduckgo,marginalia,yahoo,mojeek` if your IP is served normally.

### Keyed (enabled when configured)

- `brave`: uses Brave Search API. Set `SEARCH_MCP_BRAVE_API_KEY` or `--brave-api-key`. `count` is clamped to Brave's maximum of 20; larger requests page via `offset`.
- `searxng`: uses a SearXNG instance's JSON API. Set `SEARCH_MCP_SEARXNG_URL` or `--searxng-url`. The instance must have `format=json` enabled.
- `kagi`: uses Kagi Search API. Set `SEARCH_MCP_KAGI_API_KEY` or `--kagi-api-key`.
- `exa`: uses Exa Search API. Set `SEARCH_MCP_EXA_API_KEY` or `--exa-api-key`.
- `tavily`: uses Tavily Search API. Set `SEARCH_MCP_TAVILY_API_KEY` or `--tavily-api-key`.

The keyless providers are scrapers fighting anti-bot systems, so treat them as
best effort: expect roughly ten results per query and occasional blocks. For
consistently reliable search, configure one of the keyed providers above —
Brave has a free tier that covers ordinary personal use.

## Usage

```sh
go run . search "model context protocol"                        # fans out to every provider
go run . search "model context protocol" --provider duckduckgo  # one provider, with fallback
SEARCH_MCP_BRAVE_API_KEY=... go run . search "open telemetry go" --provider brave --count 5
go run . read https://github.com/golang/go/issues/64876
go run . serve
```

Config can be set with flags, environment variables prefixed with `SEARCH_MCP_`, or `search-mcp.yaml` in `$HOME/.config/search-mcp/` (pass `--config` to point elsewhere; the current directory is deliberately not searched).

Useful settings:

```yaml
provider: ""            # "" or "all" fans out; a name selects one provider
providers:              # keyless providers to enable
  - duckduckgo
  - marginalia
  - yahoo
brave_api_key: ""
brave_endpoint: ""
searxng_url: ""
kagi_api_key: ""
exa_api_key: ""
tavily_api_key: ""
duckduckgo_endpoint: ""
marginalia_endpoint: ""
mojeek_endpoint: ""
yahoo_endpoint: ""
rate_rps: 1
rate_burst: 2
retry_max_attempts: 3
retry_base_delay: 200ms
breaker_threshold: 5
breaker_cooldown: 30s
cache_ttl: 5m
web_cache_ttl: 15m
web_cache_dir: ""
allow_domains: []
block_domains: []
log_level: info
otel: false
otel_exporter: stdout
otel_endpoint: ""
```

## Reliability

Every provider here fails independently and often, so the defaults are built
around surviving that:

- **Fan-out by default.** With no `--provider`, a query goes to every configured provider in parallel and the rankings are merged with reciprocal rank fusion, deduplicating by normalized URL. Providers that fail are reported in the response's `degraded` list rather than silently thinning the results, so a short result set is never mistaken for a healthy one. Results are cached in memory for `cache_ttl` to keep the extra load down.
- **Fallback on any failure.** When you do name a provider, a failure of any kind — anti-bot block, rate limit, open circuit breaker, upstream 5xx, transport error, markup parse failure — moves on to the next provider. Only caller cancellation stops the chain. If every provider fails, the error names each one and its own reason.
- **Soft blocks are real errors.** Providers detect challenge pages served with a 2xx status, and treat a missing results container as a block too. That way a captcha or a change to upstream markup surfaces as an error and trips the circuit breaker, instead of masquerading as "no results found".
- **Per-provider decorators.** Transient failures are retried with exponential backoff up to `retry_max_attempts`; repeated failures trip a per-provider circuit breaker (`breaker_threshold` / `breaker_cooldown`); successful responses are cached when `cache_ttl > 0`.

## MCP tools

- `search` — run a query, fanning out across providers by default. `count` is best effort: keyless HTML backends carry roughly ten results per page.
- `search_batch` — run up to ten queries in parallel; a failed query is reported per query instead of failing the batch.
- `web_read` — fetch a URL and return Markdown, with `max_length`/`start_index` for chunked reads, `query` to grep within the page, and `links` to list its links. Several hosts are pulled through their native APIs and rendered as structured Markdown: GitHub repos / issues / pull-requests / blobs, GitLab issues and merge requests, Gerrit changes, Gitiles trees and blobs, Reddit comment threads, Hacker News items, Lobsters stories, Stack Overflow questions, Wikipedia articles, arXiv abstracts, pkg.go.dev packages, and YouTube videos with public transcripts. Everything else is fetched as HTML and converted via `html-to-markdown`, with RSS/Atom, JSON and PDF handled by content type.
- `read_pdf` — fetch a PDF and return selected page ranges or case-insensitive search matches with page numbers and optional line context. It never returns PDF bytes.
- `provider_status` — report each provider's health: whether it is usable, its circuit-breaker state, consecutive failures, cooldown remaining, the last error, and rate-limit headroom.

`serve` speaks MCP over stdio by default; `--http <addr>` serves the streamable HTTP transport instead.

Set `--otel --otel-exporter otlp` to export traces and metrics through the OpenTelemetry OTLP HTTP exporters. Standard OTEL environment variables such as `OTEL_EXPORTER_OTLP_ENDPOINT` are honored by the exporter packages.

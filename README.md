# search-mcp

Go MCP server and CLI for web search.

Provider implementations live in dedicated packages under `internal/provider/`
(`duckduckgo`, `bing`, `google`, `marginalia`, `mojeek`, `wikipedia`, `yahoo`, `brave`,
`searxng`, `kagi`, `exa`, `tavily`, and `perplexity`). Each package implements `search.Provider` and registers
its constructor from `init()`; the command imports the packages for
registration and builds only the providers enabled by configuration.

## Providers

### Free/keyless providers

By default `duckduckgo` and `yahoo` are enabled. This default set uses
browser-facing HTML pages only: no API key, subscription, or paid search API is
required. Change the set with `--providers` (or `SEARCH_MCP_PROVIDERS`), e.g.
`--providers duckduckgo,yahoo`.

- `duckduckgo`: scrapes `https://html.duckduckgo.com/html/` (the same endpoint the DuckDuckGo web UI uses). DDG aggressively rate-limits datacenter IPs and serves an anomaly/captcha page after a few requests; the provider detects this and reports it as blocked so the search falls back. Its HTML endpoint returns about ten results per page and its next-page cursor is bot-gated, so it does not paginate.
- `bing`: scrapes Bing's browser-facing HTML results page, unwraps Bing click redirects, maps country/language/safe-search/freshness filters, and pages up to three pages. It is opt-in because live CLI probes found empty or unrelated organic results on ordinary queries. The provider rejects obviously unrelated results as a block instead of merging them into the answer.
- `google`: scrapes Google's browser-facing HTML results page, including `/url` click redirects and common snippets. It is **opt-in** because Google challenges hosted/datacenter IPs more aggressively: enable it with `--providers duckduckgo,bing,google,yahoo` when it works well from your network.
- `yahoo`: scrapes Yahoo's public HTML results. Tracking links are unwrapped to their destination URLs, snippet date prefixes are lifted into `published`, and it pages via the `b` offset (up to three pages) to satisfy larger `count` values.
- `marginalia`: uses the public keyless JSON endpoint `https://api.marginalia.nu/public/search` as an optional independent fallback. It favours small, text-heavy, non-SEO-optimised sites and returns twenty results in one call. It is not in the default set because the default path is intentionally HTML-only.
- `wikipedia`: uses Wikipedia's public MediaWiki search API, with no key. It is useful for encyclopedic topics and supports `language` by searching that language's Wikipedia. Enable it with `--providers duckduckgo,yahoo,wikipedia`. It is opt-in because its encyclopedia index is a poor fit for general web and recent-news queries.
- `mojeek`: scrapes `https://www.mojeek.com/search`. **Not enabled by default** — Mojeek currently answers datacenter IPs with an HTTP 200 captcha page regardless of User-Agent, so it costs a round trip while returning nothing. Re-enable it with `--providers duckduckgo,yahoo,mojeek` if your IP is served normally.

### Keyed (enabled when configured)

- `brave`: uses Brave Search API. Set `SEARCH_MCP_BRAVE_API_KEY` or `--brave-api-key`. `count` is clamped to Brave's maximum of 20; larger requests page via `offset`.
- `searxng`: uses a SearXNG instance's JSON API. Set `SEARCH_MCP_SEARXNG_URL` or `--searxng-url`. The instance must have `format=json` enabled.
- `kagi`: uses Kagi Search API. Set `SEARCH_MCP_KAGI_API_KEY` or `--kagi-api-key`.
- `exa`: uses Exa Search API. Set `SEARCH_MCP_EXA_API_KEY` or `--exa-api-key`.
- `tavily`: uses Tavily Search API. Set `SEARCH_MCP_TAVILY_API_KEY` or `--tavily-api-key`.
- `perplexity`: uses the [Perplexity Search API](https://docs.perplexity.ai/api-reference/search-post), returning ranked URLs, query-relevant excerpts, and publication dates. Set `SEARCH_MCP_PERPLEXITY_API_KEY` or `--perplexity-api-key`. Supports country, two-letter language codes, and freshness (`pd`/`pw`/`pm`/`py` or `hour`/`day`/`week`/`month`/`year`). Web search returns at most 20 results without pagination. It requires a paid API key and is enabled only when configured; it does not call the Sonar answer-generation API. Safe-search is not supported by this backend.

The HTML providers are scrapers fighting anti-bot systems, so treat them as
best effort: expect roughly ten results per page and occasional blocks. The
service fans out by default, detects soft challenge pages, skips failed
providers, and reports them in `degraded`. The API-backed providers below are
optional compatibility integrations; with no API keys configured, the default
free path never calls them.

## Usage

```sh
go run . search "model context protocol"                        # fans out to every provider
go run . search "model context protocol" --provider duckduckgo  # one provider, with fallback
go run . search "open telemetry go" --providers duckduckgo,yahoo,wikipedia --count 5
go run . search "Go concurrency" --include-domains go.dev,pkg.go.dev --count 5
go run . search "distributed tracing" --exclude-domains pinterest.com --max-per-host 2
go run . read https://github.com/golang/go/issues/64876
go run . serve
```

Config can be set with flags, environment variables prefixed with `SEARCH_MCP_`, or `search-mcp.yaml` in `$HOME/.config/search-mcp/` (pass `--config` to point elsewhere; the current directory is deliberately not searched).

Useful settings:

```yaml
provider: ""            # "" or "all" fans out; a name selects one provider
providers:              # keyless providers to enable
  - duckduckgo
  - yahoo
  # Add bing or wikipedia for your use case:
  # - bing
  # - wikipedia
  # Add google when it is reachable from your network:
  # - google
brave_api_key: ""
brave_endpoint: ""
searxng_url: ""
kagi_api_key: ""
exa_api_key: ""
tavily_api_key: ""
perplexity_api_key: ""
perplexity_endpoint: ""
include_domains: []     # search result filter, includes subdomains
exclude_domains: []     # search result filter, exclusions win
max_per_host: 0         # set to 2 for source diversity; 0 disables
duckduckgo_endpoint: ""
bing_endpoint: ""
google_endpoint: ""
marginalia_endpoint: ""
mojeek_endpoint: ""
wikipedia_endpoint: ""
yahoo_endpoint: ""
rate_rps: 1
rate_burst: 2
provider_timeout: 8s
search_timeout: 30s
batch_timeout: 60s
read_timeout: 30s
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
http_token: ""          # required when --http binds beyond loopback
otel: false
otel_exporter: stdout
otel_endpoint: ""
```

## Result quality and source selection

CLI, MCP `search`, and MCP `search_batch` use the same result pipeline:

- Reciprocal rank fusion rewards agreement between providers. Repeated URLs
  within one provider count once, including tracking-parameter variants.
- Searches retrieve at least ten candidates per provider before returning the
  requested count, so even `count=1` can find agreement below the first rank.
  Domain filters or a host limit expand this to up to three times the requested
  count, capped at 50 candidates per provider. Individual backend limits still
  apply. This can increase pagination/API usage; the unfiltered default count of
  ten still requests ten candidates.
- Missing titles fall back to URLs; duplicate hits contribute the longest
  available snippet and fill missing titles and publication dates. Invalid URLs
  are removed in both single-provider and merged searches. CLI output shows each
  result's provider sources and publication date when supplied.
- `--include-domains`, `--exclude-domains`, and `--max-per-host` select sources
  after retrieval and ranking, before the final count limit. Use hostnames such
  as `go.dev`, without a scheme, path, or wildcard. Domain rules include
  subdomains, exclusions win, and `www.` variants share the same host limit.
  Other subdomains count separately. These are local result filters, so they
  may return fewer results and do not constrain which services receive the query.
  Use a provider-supported `site:` query when you need upstream site targeting.

MCP uses `include_domains`/`exclude_domains` arrays and `max_per_host`:

```json
{"query":"Go concurrency","include_domains":["go.dev","pkg.go.dev"],"max_per_host":2,"count":5}
```

Omitted MCP options inherit configuration; explicit empty domain arrays clear
configured filters, and `max_per_host: 0` disables a configured host limit.
Search filters are separate from `allow_domains`/`block_domains`, which control
what the page reader can fetch. Result counts are best effort (default 10,
maximum 50); negative counts or host limits are rejected.

## Reliability

Every provider here fails independently and often, so the defaults are built
around surviving that:

- **Fan-out by default.** With no `--provider`, a query goes to every configured provider in parallel and the rankings are merged with reciprocal rank fusion, deduplicating by normalized URL. Providers that fail are reported in the response's `degraded` list rather than silently thinning the results, so a short result set is never mistaken for a healthy one. Results are cached in memory for `cache_ttl` to keep the extra load down.
- **Fallback on any failure.** When you do name a provider, a failure of any kind — anti-bot block, rate limit, open circuit breaker, upstream 5xx, transport error, markup parse failure — moves on to the next provider. Successful fallback responses preserve earlier provider failures in `degraded`. Only caller cancellation stops the chain. If every provider fails, the error names each one and its own reason.
- **Soft blocks are real errors.** Providers detect challenge pages served with a 2xx status, and treat a missing results container as a block too. That way a captcha or a change to upstream markup surfaces as an error and trips the circuit breaker, instead of masquerading as "no results found".
- **Per-provider decorators.** Transient failures are retried with exponential backoff up to `retry_max_attempts`; repeated failures trip a per-provider circuit breaker (`breaker_threshold` / `breaker_cooldown`); successful responses are cached when `cache_ttl > 0`.

## MCP tools

- `search` — run a query, fanning out across providers by default. `count` is best effort: free HTML backends carry roughly ten results per page and at most three pages. Supports domain filters and per-host limits. The response includes `degraded` when a provider was unavailable, including during fallback.
- `search_batch` — run up to ten queries in parallel; returns an ordered `items` array with exactly one `{query, response}` or `{query, error}` entry per input query, including duplicates. Legacy `responses`/`errors` fields are retained for older clients, but the map cannot preserve duplicate query strings.
- `web_read` — fetch a URL and return Markdown, with `max_length`/`start_index` for chunked reads, `query` to grep within the page, and `links` to list its links. Several hosts are pulled through their native APIs and rendered as structured Markdown: GitHub repos / issues / pull-requests / blobs, GitLab issues and merge requests, Gerrit changes, Gitiles trees and blobs, Reddit comment threads, Hacker News items, Lobsters stories, Stack Overflow questions, Wikipedia articles, arXiv abstracts, pkg.go.dev packages, and YouTube videos with public transcripts. Everything else is fetched as HTML and converted via `html-to-markdown`, with RSS/Atom, JSON and PDF handled by content type.
- `read_pdf` — fetch a PDF and return selected page ranges or case-insensitive search matches with page numbers and optional line context. It never returns PDF bytes.
- `provider_status` — report each provider's health: whether it is usable, its circuit-breaker state, consecutive failures, cooldown remaining, the last error, and rate-limit headroom.

`serve` speaks MCP over stdio by default; `--http <addr>` serves the streamable HTTP transport instead. Loopback HTTP listeners are allowed without authentication. A non-loopback listener such as `:8080` or `0.0.0.0:8080` requires `--http-token TOKEN`; clients can send `Authorization: Bearer TOKEN` (or `X-Search-MCP-Token`). Request bodies are capped at 1 MiB.

Set `--otel --otel-exporter otlp` to export traces and metrics through the OpenTelemetry OTLP HTTP exporters. Standard OTEL environment variables such as `OTEL_EXPORTER_OTLP_ENDPOINT` are honored by the exporter packages.

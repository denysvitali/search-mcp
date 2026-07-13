# search-mcp

Go MCP server and CLI for web search.

Provider implementations live in dedicated packages under `internal/provider/`
(`duckduckgo`, `mojeek`, `yahoo`, `brave`, `searxng`, `kagi`, `exa`, and
`tavily`). Each package implements `search.Provider` and registers its
constructor from `init()`; the command imports the packages for registration
and builds only the providers enabled by configuration.

## Providers

- `duckduckgo`: scrapes `https://html.duckduckgo.com/html/` (the same endpoint the DuckDuckGo web UI uses). No API key required. DDG aggressively rate-limits datacenter IPs and serves an anomaly/captcha page after a few requests; the provider returns a clear error when this happens.
- `mojeek`: scrapes the public `https://www.mojeek.com/search` HTML SERP. No API key required. Mojeek's index is independent (not a Bing/Google reseller), and unlike DDG/Qwant the public web UI currently serves datacenter IPs without a bot wall — at the cost of being more brittle than a JSON API if their HTML changes.
- `yahoo`: scrapes Yahoo's public HTML results as a third no-key fallback. Tracking links are unwrapped to their destination URLs.
- `brave`: uses Brave Search API. Set `SEARCH_MCP_BRAVE_API_KEY` or `--brave-api-key`.
- `kagi`: uses Kagi Search API. Set `SEARCH_MCP_KAGI_API_KEY` or `--kagi-api-key`.
- `exa`: uses Exa Search API. Set `SEARCH_MCP_EXA_API_KEY` or `--exa-api-key`.
- `tavily`: uses Tavily Search API. Set `SEARCH_MCP_TAVILY_API_KEY` or `--tavily-api-key`.

## Usage

```sh
go run . search "model context protocol" --provider duckduckgo
SEARCH_MCP_BRAVE_API_KEY=... go run . search "open telemetry go" --provider brave --count 5
go run . read https://github.com/golang/go/issues/64876
go run . serve
```

Config can be set with flags, environment variables prefixed with `SEARCH_MCP_`, or `search-mcp.yaml`.

Useful settings:

```yaml
provider: duckduckgo
brave_api_key: ""
brave_endpoint: ""
duckduckgo_endpoint: ""
mojeek_endpoint: ""
yahoo_endpoint: ""
rate_rps: 1
rate_burst: 2
retry_max_attempts: 3
retry_base_delay: 200ms
breaker_threshold: 5
breaker_cooldown: 30s
cache_ttl: 0
otel: false
otel_exporter: stdout
otel_endpoint: ""
```

Each provider is wrapped with resilience decorators: transient failures (HTTP 429 / `ErrRateLimited`) are retried with exponential backoff up to `retry_max_attempts`; repeated failures trip a per-provider circuit breaker (`breaker_threshold` / `breaker_cooldown`); and successful responses are cached in-memory when `cache_ttl > 0`. On top of that, `search` performs automatic **fallback**: if the selected provider is rate-limited or blocked by an anti-bot challenge, the remaining providers are tried in order until one succeeds.

The MCP server exposes three tools:

- `search` — run a query through one of the providers above (with automatic fallback).
- `web_read` — fetch a URL and return Markdown. Several hosts are pulled through their native APIs and rendered as structured Markdown: GitHub repos / issues / pull-requests, Reddit comment threads, Hacker News items, Stack Overflow questions, arXiv abstracts, and YouTube videos with public transcripts. Everything else is fetched as HTML and converted via `html-to-markdown`.
- `read_pdf` — fetch a PDF and return selected page ranges or case-insensitive search matches with page numbers and optional line context. It never returns PDF bytes.

Set `--otel --otel-exporter otlp` to export traces and metrics through the OpenTelemetry OTLP HTTP exporters. Standard OTEL environment variables such as `OTEL_EXPORTER_OTLP_ENDPOINT` are honored by the exporter packages.

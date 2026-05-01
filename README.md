# search-mcp

Go MCP server and CLI for web search.

## Providers

- `duckduckgo`: scrapes `https://html.duckduckgo.com/html/` (the same endpoint the DuckDuckGo web UI uses). No API key required. DDG aggressively rate-limits datacenter IPs and serves an anomaly/captcha page after a few requests; the provider returns a clear error when this happens.
- `mojeek`: scrapes the public `https://www.mojeek.com/search` HTML SERP. No API key required. Mojeek's index is independent (not a Bing/Google reseller), and unlike DDG/Qwant the public web UI currently serves datacenter IPs without a bot wall — at the cost of being more brittle than a JSON API if their HTML changes.
- `brave`: uses Brave Search API. Set `SEARCH_MCP_BRAVE_API_KEY` or `--brave-api-key`.

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
rate_rps: 1
rate_burst: 2
otel: false
otel_exporter: stdout
```

The MCP server exposes two tools:

- `search` — run a query through one of the providers above.
- `web_read` — fetch a URL and return Markdown. GitHub repo / issue / pull-request URLs and Reddit comment threads are pulled through their respective JSON APIs; everything else is fetched as HTML and converted via `html-to-markdown`.

Set `--otel --otel-exporter otlp` to export traces and metrics through the OpenTelemetry OTLP HTTP exporters. Standard OTEL environment variables such as `OTEL_EXPORTER_OTLP_ENDPOINT` are honored by the exporter packages.

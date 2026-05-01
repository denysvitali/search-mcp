# search-mcp

Go MCP server and CLI for web search.

## Providers

- `duckduckgo`: scrapes `https://html.duckduckgo.com/html/` (the same endpoint the DuckDuckGo web UI uses). No API key required. DDG aggressively rate-limits datacenter IPs and serves an anomaly/captcha page after a few requests; the provider returns a clear error when this happens.
- `brave`: uses Brave Search API. Set `SEARCH_MCP_BRAVE_API_KEY` or `--brave-api-key`.

## Usage

```sh
go run . search "model context protocol" --provider duckduckgo
SEARCH_MCP_BRAVE_API_KEY=... go run . search "open telemetry go" --provider brave --count 5
go run . serve
```

Config can be set with flags, environment variables prefixed with `SEARCH_MCP_`, or `search-mcp.yaml`.

Useful settings:

```yaml
provider: duckduckgo
brave_api_key: ""
brave_endpoint: ""
duckduckgo_endpoint: ""
rate_rps: 1
rate_burst: 2
otel: false
otel_exporter: stdout
```

The MCP tool is named `search`.

Set `--otel --otel-exporter otlp` to export traces and metrics through the OpenTelemetry OTLP HTTP exporters. Standard OTEL environment variables such as `OTEL_EXPORTER_OTLP_ENDPOINT` are honored by the exporter packages.

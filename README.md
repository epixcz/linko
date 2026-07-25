# Linko

This is a toy URL shortener project, to be used as the starter repo for the Logging and Telemetry course on [Boot.dev](https://www.boot.dev/).

It's intentionally small, a little messy, and realistic enough to practice adding logs, metrics, and traces in Go.

## Motivation

Linko provides a small, realistic Go service for learning how to add and operate logs, metrics, and distributed traces. It includes authentication, persistent URL storage, redirects, Prometheus metrics, profiling endpoints, and OpenTelemetry tracing without the complexity of a production application.

## Quick Start

Linko requires Go 1.26 or later.

```bash
git clone <repository-url>
cd linko-starter
go mod download
go run .
```

The server starts at [http://localhost:8899](http://localhost:8899) and stores its data in `./data`. Use `Ctrl+C` to stop it.

You can change the port and data directory with command-line flags:

```bash
go run . -port 8080 -data ./linko-data
```

## Usage

Open the root URL in a browser to use the web interface. The development server also exposes an HTTP API. Protected endpoints use HTTP Basic authentication; the included development account is `frodo` with password `ofTheNineFingers`.

Shorten a URL:

```bash
curl -u frodo:ofTheNineFingers \
  -X POST \
  -d 'url=https://www.boot.dev/' \
  http://localhost:8899/api/shorten
```

Use the returned short code by visiting `http://localhost:8899/<short-code>`.

Other useful endpoints include:

- `GET /api/urls` — list stored short URLs (authentication required)
- `GET /api/stats` — view redirect statistics (authentication required)
- `GET /metrics` — view Prometheus metrics
- `GET /debug/pprof/` — view Go profiling data (authentication required)

Set `LINKO_LOG_FILE` to write JSON logs to a rotating file in addition to stderr. Set `ENV=production` to disable the development shutdown endpoint. OpenTelemetry export can be configured with the standard `OTEL_EXPORTER_OTLP_*` environment variables.

## Contributing

Contributions are welcome. Before submitting a change:

1. Create a branch for your work.
2. Format Go code with `gofmt`.
3. Run the test suite with `go test ./...`.
4. Open a pull request that explains the change and why it is useful.

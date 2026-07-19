# slogx

[![CI](https://github.com/latnikovs/slogx/actions/workflows/ci.yml/badge.svg)](https://github.com/latnikovs/slogx/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/latnikovs/slogx.svg)](https://pkg.go.dev/github.com/latnikovs/slogx)

A tiny, **dependency-free** configuration layer for the standard library's
[`log/slog`](https://pkg.go.dev/log/slog). It gives you the two output modes a
typical service actually needs:

- **Structured** — [Google Cloud Logging](https://cloud.google.com/logging)
  JSON on stdout, with `severity` mapping and request-trace correlation
  (`logging.googleapis.com/trace`) so log lines group under their Cloud Run
  request in the Logs Explorer.
- **Plain** — pretty, colorized, human-readable text on stdout.

The modes are named for the output format they produce, not for a deployment
environment — the library never owns your environment names.

Standard library only — no logging framework, no transitive dependencies.

## Install

```sh
go get github.com/latnikovs/slogx
```

## Usage

Call `Setup` once at startup; it installs the process-wide `slog` default, so
the rest of your code just uses `slog.Info`, `slog.Error`, etc.

```go
package main

import (
	"log/slog"

	"github.com/latnikovs/slogx"
)

func main() {
	// projectID is your GCP project, used to build the trace field.
	// It is ignored in Plain mode.
	slogx.Setup(slogx.Structured, "my-gcp-project")

	slog.Info("server starting", "port", 8080)
}
```

### Modes, not environments

slogx owns exactly two **output modes** — `Plain` and `Structured` — named for
the output format they produce. It deliberately does *not* model your deployment
environments. A project with `staging`, `qa`, and `prod` maps its own vocabulary
onto the two modes.

Pass the mode explicitly (recommended when you have more than two environments):

```go
mode := slogx.Structured
if cfg.Env == "local" {
	mode = slogx.Plain
}
slogx.Setup(mode, cfg.ProjectID)
```

…or use the `ModeFromEnv` convenience mapper, which treats only local-ish values
(`""`, `dev`, `development`, `local`) as `Plain` and **everything else** as
`Structured`. Defaulting the unknown case to `Structured` means anything
deployed emits machine-readable logs even under an environment name slogx does
not recognize. `Structured` is also the zero value, so a mistakenly unset `Mode`
still fails safe:

```go
slogx.Setup(slogx.ModeFromEnv(os.Getenv("ENV")), cfg.ProjectID)
```

### Request-trace correlation

In production, populate the request context with the inbound trace once (e.g.
from Cloud Run's `X-Cloud-Trace-Context` header) in middleware, and every log
line emitted with that context is grouped under the request's trace:

```go
ctx := slogx.ContextWithTrace(r.Context(), traceID, spanID)
r = r.WithContext(ctx)
// ...later, anywhere down the stack:
slog.InfoContext(r.Context(), "handled request")
```

The `slogx/tracehttp` subpackage does the inbound half for you — parse the
Cloud Run header and populate the context in one middleware:

```go
import "github.com/latnikovs/slogx/tracehttp"

mux := http.NewServeMux()
// ...
handler := tracehttp.Middleware()(mux)
```

It parses `X-Cloud-Trace-Context` (`TRACE_ID/SPAN_ID;o=1`), converts the span to
the hex form Cloud Logging expects, and stores the trace via
`slogx.ContextWithTraceInfo` — so downstream logs emit
`logging.googleapis.com/trace`, `spanId`, and `trace_sampled`. A missing or
malformed header is not an error: the request passes through with no trace.
`ParseCloudTraceHeader` is exported for reuse. The core `slogx` package imports
no `net/http`; HTTP lives only in this subpackage.

### Critical severity

`slogx.LevelCritical` is a level above `slog.LevelError`; in production it
renders as Cloud Logging's `CRITICAL` severity:

```go
slog.LogAttrs(ctx, slogx.LevelCritical, "data loss detected")
```

### Error Reporting

In `Structured` mode, records at or above the reporting threshold (default
`slog.LevelError`) are automatically enriched so they surface in
[Cloud Error Reporting](https://cloud.google.com/error-reporting) — grouped,
counted and alertable — not just in the Logs Explorer. Each qualifying record
gets the `ReportedErrorEvent` `@type`, a `serviceContext` object, and a
synthesized `stack_trace` (when it carries none). This is **on by default**
where it makes sense and automatically **off** in `Plain` mode — no per-service
flag to remember.

```go
slogx.Setup(slogx.Structured, "my-gcp-project") // Error Reporting on by default

slog.ErrorContext(ctx, "charge failed", "order", id) // → grouped in Error Reporting
```

`serviceContext` resolves `service`/`version` from Cloud Run's `K_SERVICE` /
`K_REVISION`, degrading gracefully when they are unset. Tune the behaviour with
functional options (the two-argument `Setup` call keeps working unchanged):

```go
slogx.Setup(slogx.Structured, "my-gcp-project",
    slogx.WithReportThreshold(slogx.LevelCritical), // report only critical records
    slogx.WithServiceContext("example-service", "v1.4.0"), // override K_SERVICE/K_REVISION
    slogx.WithoutErrorReporting(),                  // rare full opt-out
)
```

Opt a single, known-noisy record out with the `slogx.NoReport` marker — it logs
at its normal level but never spawns an Error Reporting group, and the marker
itself is stripped from the output:

```go
slog.Error("expected upstream 429, retrying", slogx.NoReport)
```

## Versioning

Semantic versioning via git tags. `go get` fetches tagged releases directly —
no registry, no publish step.

## License

[MIT](LICENSE)

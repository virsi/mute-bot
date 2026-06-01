// Package obs centralises cross-cutting observability primitives: a
// structured logger constructor, Prometheus metric registration helpers,
// and an OpenTelemetry tracing bootstrap.
//
// The package is intentionally dependency-light. Application code never
// imports the obs package transitively through business logic — each
// process wires it once at startup and passes the resulting *slog.Logger
// or *Metrics down through constructors.
package obs

import (
	"log/slog"
	"os"
)

// NewLogger returns a JSON slog.Logger pre-tagged with the component name.
// Every process should call this once at startup and pass the result to
// slog.SetDefault so package-level slog.Info/Error calls inherit the tag.
//
// The "component" attribute is what lets us filter logs in Loki / Grafana
// by the producing binary (session-reader / processor / bot-api / scheduler)
// without having to inspect the source file.
func NewLogger(level slog.Level, component string) *slog.Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level, AddSource: false})
	return slog.New(h).With("component", component)
}

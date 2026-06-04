package obs

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the closed set of Prometheus collectors used by the digest
// pipeline. It is constructed once per process at startup and shared by
// pointer across worker goroutines — every collector type in client_golang
// is documented as goroutine-safe, so no extra synchronisation is needed.
//
// Naming follows the Prometheus convention: snake_case names, _total
// suffix on counters, _seconds on time histograms, _ratio for unitless
// 0..1 gauges. Labels are kept low-cardinality on purpose: "channel" on
// IngestPosts is the source channel name, not per-post; "purpose" on LLM
// counters is one of {classify, embed, merge}; never user_id or post_id.
type Metrics struct {
	// Ingest stage.
	IngestPosts  *prometheus.CounterVec // labels: channel
	PostsDropped *prometheus.CounterVec // labels: reason

	// Dedup stage.
	DedupMatchKind *prometheus.CounterVec // labels: kind (minhash|embedding|llm|new)

	// Cluster lifecycle.
	ClusterSize     prometheus.Histogram
	ClusterLifetime prometheus.Histogram

	// LLM provider.
	LLMCalls   *prometheus.CounterVec // labels: purpose
	LLMTokens  *prometheus.CounterVec // labels: purpose
	LLMCostUSD *prometheus.CounterVec // labels: purpose

	// Digest delivery.
	DigestSent     *prometheus.CounterVec // labels: tier, channel
	DigestAssemble prometheus.Histogram

	// Steady-state state gauges.
	SubscriptionActive *prometheus.GaugeVec // labels: tier
	LLMBudgetUsedRatio prometheus.Gauge
	QueueDepth         *prometheus.GaugeVec // labels: stream
	CBState            *prometheus.GaugeVec // labels: component
	TrialActive        prometheus.Gauge     // count of active trial subscriptions
}

// NewMetrics constructs and registers every collector against reg. Passing
// a fresh prometheus.NewRegistry() in tests keeps the global default
// registry uncluttered between table-driven test cases. In production each
// process passes prometheus.DefaultRegisterer so /metrics returns these
// alongside the runtime collectors auto-registered by client_golang.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	return &Metrics{
		IngestPosts:        cv(reg, "ingest_posts_total", "Raw posts ingested", "channel"),
		PostsDropped:       cv(reg, "posts_dropped_total", "Posts dropped", "reason"),
		DedupMatchKind:     cv(reg, "dedup_match_kind_total", "Dedup outcomes", "kind"),
		ClusterSize:        h(reg, "cluster_size", "Cluster size when classified", prometheus.LinearBuckets(1, 1, 20)),
		ClusterLifetime:    h(reg, "cluster_lifetime_seconds", "Cluster active lifetime", prometheus.ExponentialBuckets(60, 2, 10)),
		LLMCalls:           cv(reg, "llm_calls_total", "LLM calls", "purpose"),
		LLMTokens:          cv(reg, "llm_tokens_total", "LLM tokens", "purpose"),
		LLMCostUSD:         cv(reg, "llm_cost_usd_total", "LLM cost USD", "purpose"),
		DigestSent:         cv(reg, "digest_sent_total", "Digests sent", "tier", "channel"),
		DigestAssemble:     h(reg, "digest_assemble_seconds", "Digest assembly time", prometheus.DefBuckets),
		SubscriptionActive: gv(reg, "subscription_active_gauge", "Active subscriptions", "tier"),
		LLMBudgetUsedRatio: g(reg, "llm_budget_used_ratio", "Fraction of monthly LLM budget used"),
		QueueDepth:         gv(reg, "queue_depth", "Queue depth", "stream"),
		CBState:            gv(reg, "cb_state", "Circuit breaker state", "component"),
		TrialActive:        g(reg, "trial_active_gauge", "Active trial subscriptions"),
	}
}

// cv registers a CounterVec with the given name/help/labels. It panics via
// MustRegister on duplicate names — that is intentional: a duplicate
// indicates a programming error that must fail loudly at startup, not at
// scrape time.
func cv(reg prometheus.Registerer, name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	reg.MustRegister(c)
	return c
}

// gv registers a GaugeVec. See cv on the panic semantics.
func gv(reg prometheus.Registerer, name, help string, labels ...string) *prometheus.GaugeVec {
	gg := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
	reg.MustRegister(gg)
	return gg
}

// g registers a label-less Gauge.
func g(reg prometheus.Registerer, name, help string) prometheus.Gauge {
	gg := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
	reg.MustRegister(gg)
	return gg
}

// h registers a Histogram with explicit buckets. Buckets are passed by the
// caller because the right boundaries are domain-specific (e.g. cluster
// size uses linear buckets, lifetime uses exponential ones).
func h(reg prometheus.Registerer, name, help string, buckets []float64) prometheus.Histogram {
	hh := prometheus.NewHistogram(prometheus.HistogramOpts{Name: name, Help: help, Buckets: buckets})
	reg.MustRegister(hh)
	return hh
}

// ServeMetrics starts an HTTP server exposing /metrics (Prometheus
// exposition) and /healthz (200 OK liveness). It returns the *http.Server
// so callers can Shutdown it during graceful termination.
//
// The server runs in a background goroutine. A bind failure is silently
// dropped by the goroutine — that is intentional for Phase 1: the metrics
// endpoint is a side-channel, and we do not want a port collision in a
// dev machine to take down the actual worker. In production the readiness
// probe on /healthz will fail loudly if the server never came up.
func ServeMetrics(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.ListenAndServe() }()
	return srv
}

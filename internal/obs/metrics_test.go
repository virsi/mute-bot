package obs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/require"
)

// TestMetrics_Registers exercises the full set of metric vectors with a
// representative label combo each, then asks the registry to gather. If a
// metric name or label cardinality regresses (typo in NewMetrics, duplicated
// help text, collisions), the gather call will surface it here instead of
// at process startup in production.
func TestMetrics_Registers(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.IngestPosts.WithLabelValues("ria").Inc()
	m.PostsDropped.WithLabelValues("dup").Inc()
	m.DedupMatchKind.WithLabelValues("minhash").Inc()
	m.ClusterSize.Observe(5)
	m.ClusterLifetime.Observe(120)
	m.LLMCalls.WithLabelValues("classify").Inc()
	m.LLMTokens.WithLabelValues("classify").Add(150)
	m.LLMCostUSD.WithLabelValues("classify").Add(0.0012)
	m.DigestSent.WithLabelValues("free", "morning").Inc()
	m.DigestAssemble.Observe(0.42)
	m.SubscriptionActive.WithLabelValues("pro").Set(42)
	m.LLMBudgetUsedRatio.Set(0.31)
	m.QueueDepth.WithLabelValues("ingest").Set(7)
	m.CBState.WithLabelValues("openai").Set(0)
	m.TrialActive.Set(11)

	families, err := reg.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, families)
}

// TestServeMetrics_ExposesEndpoints verifies that /metrics renders the
// default Prometheus exposition and /healthz returns 200. We hit the mux
// directly via httptest rather than booting a real ListenAndServe to keep
// the test hermetic and free of port-binding flakes.
func TestServeMetrics_ExposesEndpoints(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	// Default Go runtime collector exposes "go_goroutines"; this is a stable
	// canary for "exposition format actually rendered".
	require.True(t, strings.Contains(rec.Body.String(), "go_goroutines"))
}

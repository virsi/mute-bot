//go:build integration

package eval

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/dedup"
)

// TestDedup_GoldenPrecisionRecall evaluates the MinHash-only deduplication
// baseline against the hand-labelled golden corpus.
//
// Skipped unless EVAL=1 to keep CI fast. The corpus is expected to grow
// during dogfooding; thresholds below are the baseline Phase 1 targets.
func TestDedup_GoldenPrecisionRecall(t *testing.T) {
	if os.Getenv("EVAL") != "1" {
		t.Skip("set EVAL=1 to run the dedup precision/recall evaluation")
	}

	recs, err := LoadCorpus("../fixtures/posts_corpus_v1.jsonl")
	require.NoError(t, err)
	require.NotEmpty(t, recs, "golden corpus is empty — populate tests/fixtures/posts_corpus_v1.jsonl before running EVAL")

	mh := dedup.NewMinHash(dedup.MinHashConfig{NumHashes: 128, ShingleSize: 5})

	gold := pairsByCluster(recs)
	predicted := map[[2]string]bool{}
	for i := range recs {
		for j := i + 1; j < len(recs); j++ {
			s1 := mh.Sign(recs[i].Text)
			s2 := mh.Sign(recs[j].Text)
			if dedup.Jaccard(s1, s2) >= 0.5 {
				predicted[pair(recs[i].ID, recs[j].ID)] = true
			}
		}
	}

	prec, rec := precisionRecall(predicted, gold)
	t.Logf("precision=%.3f recall=%.3f (gold_pairs=%d predicted_pairs=%d)",
		prec, rec, len(gold), len(predicted))
	require.GreaterOrEqual(t, prec, 0.90, "MinHash precision below baseline; tune shingle/threshold or upgrade pipeline")
	require.GreaterOrEqual(t, rec, 0.70, "MinHash recall below baseline; lower Jaccard threshold or add embeddings")
}

func pair(a, b string) [2]string {
	if a < b {
		return [2]string{a, b}
	}
	return [2]string{b, a}
}

func pairsByCluster(recs []GoldRecord) map[[2]string]bool {
	by := map[string][]string{}
	for _, r := range recs {
		if r.ClusterLabel == "" {
			continue
		}
		by[r.ClusterLabel] = append(by[r.ClusterLabel], r.ID)
	}
	out := map[[2]string]bool{}
	for _, ids := range by {
		for i := range ids {
			for j := i + 1; j < len(ids); j++ {
				out[pair(ids[i], ids[j])] = true
			}
		}
	}
	return out
}

func precisionRecall(pred, gold map[[2]string]bool) (precision, recall float64) {
	tp := 0
	fp := 0
	for p := range pred {
		if gold[p] {
			tp++
		} else {
			fp++
		}
	}
	fn := 0
	for g := range gold {
		if !pred[g] {
			fn++
		}
	}
	denomP := tp + fp
	denomR := tp + fn
	if denomP == 0 {
		precision = 0
	} else {
		precision = float64(tp) / float64(denomP)
	}
	if denomR == 0 {
		recall = 0
	} else {
		recall = float64(tp) / float64(denomR)
	}
	return precision, recall
}

// Package dedup implements the news deduplication pipeline: MinHash + LSH
// fast candidate generation, embedding-based nearest-neighbor matching, and
// an optional LLM judge for borderline cases.
package dedup

import (
	"hash/fnv"
	"math"
	"strings"
)

// MinHashConfig parameterises the MinHash sketch.
//
// NumHashes is the signature length — more hashes = higher Jaccard accuracy
// at higher compute cost. ShingleSize is the word-shingle width — longer
// shingles make the signature more discriminative but reduce recall for
// paraphrases.
type MinHashConfig struct {
	NumHashes   int
	ShingleSize int
}

// MinHash computes word-shingle MinHash signatures over UTF-8 text.
type MinHash struct {
	cfg MinHashConfig
}

// Signature is a MinHash signature — one uint32 per hash function.
type Signature []uint32

// NewMinHash constructs a MinHash with sensible defaults applied when the
// config zero-values fields.
func NewMinHash(cfg MinHashConfig) *MinHash {
	if cfg.NumHashes <= 0 {
		cfg.NumHashes = 128
	}
	if cfg.ShingleSize <= 0 {
		cfg.ShingleSize = 5
	}
	return &MinHash{cfg: cfg}
}

// Sign computes the MinHash signature of text. The text is lowercased and
// split into word shingles before hashing.
func (m *MinHash) Sign(text string) Signature {
	tokens := tokenize(strings.ToLower(text))
	shingles := shingleWords(tokens, m.cfg.ShingleSize)
	sig := make(Signature, m.cfg.NumHashes)
	for i := range sig {
		sig[i] = math.MaxUint32
	}
	for s := range shingles {
		base := fnv32(s)
		for i := 0; i < m.cfg.NumHashes; i++ {
			h := base*uint32(2654435761) + uint32(i)*uint32(40503)
			if h < sig[i] {
				sig[i] = h
			}
		}
	}
	return sig
}

// Jaccard returns the estimated Jaccard similarity of a and b — the fraction
// of signature positions where the two signatures agree. Returns 0 when the
// signatures are empty or differ in length.
func Jaccard(a, b Signature) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	eq := 0
	for i := range a {
		if a[i] == b[i] {
			eq++
		}
	}
	return float64(eq) / float64(len(a))
}

// tokenize splits s on common punctuation and whitespace, dropping empty
// runs. It is intentionally simple — anything more clever should live in the
// normalize package.
func tokenize(s string) []string {
	var out []string
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\n', '\t', '.', ',', ':', ';', '!', '?':
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// shingleWords returns the set of k-word shingles formed from tokens. When
// there are fewer than k tokens the entire token list is returned as a
// single shingle — guarantees a non-empty signature for short posts.
func shingleWords(tokens []string, k int) map[string]struct{} {
	out := make(map[string]struct{}, len(tokens))
	if len(tokens) < k {
		out[strings.Join(tokens, " ")] = struct{}{}
		return out
	}
	for i := 0; i+k <= len(tokens); i++ {
		out[strings.Join(tokens[i:i+k], " ")] = struct{}{}
	}
	return out
}

// fnv32 returns the FNV-1a hash of s as a uint32. Used as the base hash from
// which the NumHashes permutations are derived via linear combinations.
func fnv32(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

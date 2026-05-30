package dedup

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMinHash_SignaturesSimilarForSimilarText(t *testing.T) {
	mh := NewMinHash(MinHashConfig{NumHashes: 128, ShingleSize: 5})
	s1 := mh.Sign("Президент подписал указ о новом налоге")
	s2 := mh.Sign("Президент подписал указ о новом налоге сегодня")
	require.GreaterOrEqual(t, Jaccard(s1, s2), 0.7)
}

func TestMinHash_SignaturesDifferentForDifferentText(t *testing.T) {
	mh := NewMinHash(MinHashConfig{NumHashes: 128, ShingleSize: 5})
	s1 := mh.Sign("Президент подписал указ о новом налоге")
	s2 := mh.Sign("Биткоин обвалился на пятьдесят процентов")
	require.LessOrEqual(t, Jaccard(s1, s2), 0.2)
}

func TestMinHash_Identical(t *testing.T) {
	mh := NewMinHash(MinHashConfig{NumHashes: 128, ShingleSize: 5})
	s1 := mh.Sign("Hello world hello hello")
	s2 := mh.Sign("Hello world hello hello")
	require.InDelta(t, 1.0, Jaccard(s1, s2), 0.001)
}

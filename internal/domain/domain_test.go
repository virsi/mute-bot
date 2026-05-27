package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPostHash_DeterministicForSameText(t *testing.T) {
	p1 := NormalizedPost{TextClean: "Hello world"}
	p2 := NormalizedPost{TextClean: "Hello world"}
	require.Equal(t, p1.Hash(), p2.Hash())
}

func TestPostHash_DiffersForDifferentText(t *testing.T) {
	p1 := NormalizedPost{TextClean: "Hello world"}
	p2 := NormalizedPost{TextClean: "Hello there"}
	require.NotEqual(t, p1.Hash(), p2.Hash())
}

func TestClusterAge(t *testing.T) {
	c := Cluster{FirstSeenAt: time.Now().Add(-2 * time.Hour)}
	require.InDelta(t, (2 * time.Hour).Seconds(), c.Age().Seconds(), 5)
}

func TestPresetTopics_HasExpectedIDs(t *testing.T) {
	ids := make(map[string]struct{}, len(PresetTopics))
	for _, tp := range PresetTopics {
		ids[tp.ID] = struct{}{}
	}
	for _, want := range []string{"politics", "it", "crypto", "economy", "war", "science", "sport"} {
		_, ok := ids[want]
		require.Truef(t, ok, "preset topics missing %q", want)
	}
}

func TestUserTier_Constants(t *testing.T) {
	require.Equal(t, Tier("free"), TierFree)
	require.Equal(t, Tier("pro"), TierPro)
}

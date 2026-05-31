package digest

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFormat_GroupsByTopic(t *testing.T) {
	now := time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC)
	items := []Item{
		{ClusterID: 1, Headline: "Big political news", Summary: "Foo.", Topics: []string{"politics"}, Sources: []string{"ria", "mash"}, Score: 90},
		{ClusterID: 2, Headline: "Crypto thing", Summary: "Bar.", Topics: []string{"crypto"}, Sources: []string{"x"}, Score: 70},
		{ClusterID: 3, Headline: "Another political", Summary: "Baz.", Topics: []string{"politics"}, Sources: []string{"y"}, Score: 60},
	}
	out := Format(items, FormatOptions{Now: now, Title: "Утренняя сводка"})
	require.Contains(t, out, "🗞 Утренняя сводка")
	require.Contains(t, out, "📌 Политика")
	require.Contains(t, out, "📌 Крипта")
	idxBig := strings.Index(out, "Big political news")
	idxAnother := strings.Index(out, "Another political")
	idxCrypto := strings.Index(out, "Crypto thing")
	require.True(t, idxBig >= 0 && idxAnother >= 0 && idxCrypto >= 0)
	require.True(t, idxBig < idxAnother && idxAnother < idxCrypto,
		"expected politics block (sorted by score) before crypto block: big=%d another=%d crypto=%d",
		idxBig, idxAnother, idxCrypto)
}

func TestFormat_Empty(t *testing.T) {
	out := Format(nil, FormatOptions{Now: time.Now(), Title: "Утренняя сводка"})
	require.Empty(t, out)
}

func TestFormat_PreservesTopicInsertionOrder(t *testing.T) {
	now := time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC)
	items := []Item{
		{ClusterID: 1, Headline: "Crypto first", Topics: []string{"crypto"}, Score: 10},
		{ClusterID: 2, Headline: "Politics next", Topics: []string{"politics"}, Score: 99},
		{ClusterID: 3, Headline: "Crypto again", Topics: []string{"crypto"}, Score: 99},
	}
	out := Format(items, FormatOptions{Now: now, Title: "T"})
	idxCryptoHeader := strings.Index(out, "📌 Крипта")
	idxPoliticsHeader := strings.Index(out, "📌 Политика")
	require.True(t, idxCryptoHeader >= 0 && idxPoliticsHeader >= 0)
	require.Less(t, idxCryptoHeader, idxPoliticsHeader,
		"crypto was inserted first so its block must come first regardless of score")
}

func TestFormat_FormatsSources(t *testing.T) {
	now := time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC)
	items := []Item{
		{ClusterID: 1, Headline: "h", Summary: "s", Topics: []string{"politics"}, Sources: []string{"a", "b"}},
		{ClusterID: 2, Headline: "h2", Summary: "s2", Topics: []string{"crypto"}, Sources: nil},
	}
	out := Format(items, FormatOptions{Now: now, Title: "T"})
	require.Contains(t, out, "@a, @b")
	require.Contains(t, out, "—")
}

package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadCorpus_Empty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "c.jsonl")
	require.NoError(t, os.WriteFile(p, []byte(""), 0o600))

	recs, err := LoadCorpus(p)
	require.NoError(t, err)
	require.Empty(t, recs)
}

func TestLoadCorpus_Parses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "c.jsonl")
	require.NoError(t, os.WriteFile(p,
		[]byte(`{"id":"a","channel":"x","text":"y","cluster_label":"c1","severity_label":2}`+"\n"),
		0o600))

	recs, err := LoadCorpus(p)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "a", recs[0].ID)
	require.Equal(t, "c1", recs[0].ClusterLabel)
	require.Equal(t, 2, recs[0].SeverityLabel)
}

func TestLoadCorpus_SkipsBlankLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "c.jsonl")
	require.NoError(t, os.WriteFile(p,
		[]byte("\n"+
			`{"id":"a","cluster_label":"c1"}`+"\n"+
			"\n"+
			`{"id":"b","cluster_label":"c1"}`+"\n",
		), 0o600))

	recs, err := LoadCorpus(p)
	require.NoError(t, err)
	require.Len(t, recs, 2)
}

func TestLoadCorpus_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := LoadCorpus(filepath.Join(t.TempDir(), "nope.jsonl"))
	require.Error(t, err)
}

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
postgres_dsn: "postgres://u:p@localhost/db"
redis_addr:   "localhost:6379"
nats_url:     "nats://localhost:4222"
bot_token:    "xxx"
openai_api_key: "sk-test"
mtproto:
  api_id: 12345
  api_hash: "abcdef"
  session_path: ".session"
channels_file: "channels.yaml"
llm:
  monthly_budget_usd: 50
`), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "postgres://u:p@localhost/db", cfg.PostgresDSN)
	require.Equal(t, 12345, cfg.MTProto.APIID)
	require.Equal(t, 50.0, cfg.LLM.MonthlyBudgetUSD)
}

func TestLoad_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
postgres_dsn: "postgres://u:p@localhost/db"
redis_addr:   "localhost:6379"
nats_url:     "nats://localhost:4222"
bot_token: file_value
`), 0o600))
	t.Setenv("MUTE_BOT_TOKEN", "env_value")
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "env_value", cfg.BotToken)
}

func TestLoad_MissingRequired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o600))
	_, err := Load(path)
	require.ErrorContains(t, err, "postgres_dsn is required")
}

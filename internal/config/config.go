// Package config loads the root YAML configuration shared by every binary
// and applies environment-variable overrides (and ${VAR} expansion).
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the root YAML configuration shared by every binary.
type Config struct {
	PostgresDSN  string  `yaml:"postgres_dsn"`
	RedisAddr    string  `yaml:"redis_addr"`
	NATSURL      string  `yaml:"nats_url"`
	BotToken     string  `yaml:"bot_token"`
	OpenAIAPIKey string  `yaml:"openai_api_key"`
	ChannelsFile string  `yaml:"channels_file"`
	OTLPEndpoint string  `yaml:"otlp_endpoint"`
	MTProto      MTProto `yaml:"mtproto"`
	LLM          LLM     `yaml:"llm"`

	// BaseExternalURL is the public HTTPS origin the bot-api process is
	// reachable at (e.g. "https://bot.example.com"). YooKassa needs an
	// absolute URL to POST notifications to; cmd/bot-api composes
	// BaseExternalURL + "/yookassa/webhook" when registering payments.
	// Optional — empty disables the YooKassa provider entirely.
	BaseExternalURL string `yaml:"base_external_url"`

	// YooKassa groups the API credentials and HMAC secret for the YooKassa
	// payment provider. Operator-controlled — never committed.
	YooKassa YooKassa `yaml:"yookassa"`
}

// YooKassa groups the API credentials and signing secret for the YooKassa
// payment provider. Empty ShopID disables the provider so a deployment
// running only Stars still boots.
type YooKassa struct {
	// ShopID is the YooKassa shop identifier used as the HTTP Basic auth
	// username when calling api.yookassa.ru.
	ShopID string `yaml:"shop_id"`
	// SecretKey is the YooKassa API secret used as the HTTP Basic auth
	// password.
	SecretKey string `yaml:"secret_key"`
	// WebhookSecret is the HMAC SHA256 key shared with YooKassa for
	// /yookassa/webhook request authentication.
	WebhookSecret string `yaml:"webhook_secret"`
	// ReturnURL is the deeplink YooKassa redirects the user to after the
	// payment confirmation page. Defaults to https://t.me/ when empty.
	ReturnURL string `yaml:"return_url"`
}

// MTProto groups MTProto user-session credentials.
type MTProto struct {
	APIID       int    `yaml:"api_id"`
	APIHash     string `yaml:"api_hash"`
	SessionPath string `yaml:"session_path"`
}

// LLM groups OpenAI-compatible client settings and per-month budget.
type LLM struct {
	MonthlyBudgetUSD float64 `yaml:"monthly_budget_usd"`
	EmbeddingModel   string  `yaml:"embedding_model"`
	ClassifierModel  string  `yaml:"classifier_model"`
	BaseURL          string  `yaml:"openai_base_url"`
}

// Load reads, env-expands and validates the YAML at path.
func Load(path string) (*Config, error) {
	// #nosec G304 -- path comes from --config flag, operator-controlled
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	expanded, err := expandEnv(raw)
	if err != nil {
		return nil, fmt.Errorf("expand env: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	applyEnvOverrides(&cfg)
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// envRefRe matches shell-style ${VAR} references with an upper-case-only name
// (digits and underscore allowed after the first char). Lower-case names are
// deliberately excluded so casual literal "$" use in URLs doesn't trigger
// expansion — only the explicit ${VAR} form does.
var envRefRe = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)

// expandEnv replaces every ${VAR} reference in in with os.LookupEnv(VAR). If
// any referenced variable is missing it returns an error listing them all —
// silent expansion to empty would produce malformed DSNs and other gnarly
// downstream errors.
func expandEnv(in []byte) ([]byte, error) {
	var missing []string
	out := envRefRe.ReplaceAllFunc(in, func(match []byte) []byte {
		name := string(envRefRe.FindSubmatch(match)[1])
		v, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
			return match
		}
		return []byte(v)
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("env vars not set: %v", missing)
	}
	return out, nil
}

func applyEnvOverrides(c *Config) {
	overrides := map[string]*string{
		"MUTE_POSTGRES_DSN":    &c.PostgresDSN,
		"MUTE_REDIS_ADDR":      &c.RedisAddr,
		"MUTE_NATS_URL":        &c.NATSURL,
		"MUTE_BOT_TOKEN":       &c.BotToken,
		"MUTE_OPENAI_API_KEY":  &c.OpenAIAPIKey,
		"MUTE_OPENAI_BASE_URL": &c.LLM.BaseURL,
		"MUTE_OTLP_ENDPOINT":   &c.OTLPEndpoint,
		// YooKassa credentials may flow in via env so secrets do not have
		// to live in the committed config.yaml. Empty values keep the
		// provider disabled.
		"MUTE_BASE_EXTERNAL_URL":       &c.BaseExternalURL,
		"MUTE_YOOKASSA_SHOP_ID":        &c.YooKassa.ShopID,
		"MUTE_YOOKASSA_SECRET_KEY":     &c.YooKassa.SecretKey,
		"MUTE_YOOKASSA_WEBHOOK_SECRET": &c.YooKassa.WebhookSecret,
		"MUTE_YOOKASSA_RETURN_URL":     &c.YooKassa.ReturnURL,
	}
	for k, p := range overrides {
		if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
			*p = v
		}
	}
}

func (c *Config) validate() error {
	var errs []error
	if c.PostgresDSN == "" {
		errs = append(errs, errors.New("postgres_dsn is required"))
	}
	if c.RedisAddr == "" {
		errs = append(errs, errors.New("redis_addr is required"))
	}
	if c.NATSURL == "" {
		errs = append(errs, errors.New("nats_url is required"))
	}
	if c.LLM.EmbeddingModel == "" {
		c.LLM.EmbeddingModel = "text-embedding-3-small"
	}
	if c.LLM.ClassifierModel == "" {
		c.LLM.ClassifierModel = "gpt-4o-mini"
	}
	if c.LLM.MonthlyBudgetUSD == 0 {
		c.LLM.MonthlyBudgetUSD = 50
	}
	return errors.Join(errs...)
}

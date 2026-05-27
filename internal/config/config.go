package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	PostgresDSN  string  `yaml:"postgres_dsn"`
	RedisAddr    string  `yaml:"redis_addr"`
	NATSURL      string  `yaml:"nats_url"`
	BotToken     string  `yaml:"bot_token"`
	OpenAIAPIKey string  `yaml:"openai_api_key"`
	ChannelsFile string  `yaml:"channels_file"`
	MTProto      MTProto `yaml:"mtproto"`
	LLM          LLM     `yaml:"llm"`
}

type MTProto struct {
	APIID       int    `yaml:"api_id"`
	APIHash     string `yaml:"api_hash"`
	SessionPath string `yaml:"session_path"`
}

type LLM struct {
	MonthlyBudgetUSD float64 `yaml:"monthly_budget_usd"`
	EmbeddingModel   string  `yaml:"embedding_model"`
	ClassifierModel  string  `yaml:"classifier_model"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	applyEnvOverrides(&cfg)
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyEnvOverrides(c *Config) {
	overrides := map[string]*string{
		"MUTE_POSTGRES_DSN":   &c.PostgresDSN,
		"MUTE_REDIS_ADDR":     &c.RedisAddr,
		"MUTE_NATS_URL":       &c.NATSURL,
		"MUTE_BOT_TOKEN":      &c.BotToken,
		"MUTE_OPENAI_API_KEY": &c.OpenAIAPIKey,
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

package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"amp-sentinel/project"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration for Amp Sentinel.
type Config struct {
	Amp       AmpConfig              `yaml:"amp"`
	Scheduler SchedulerConfig        `yaml:"scheduler"`
	Intake    IntakeConfig           `yaml:"intake"`
	Diagnosis DiagnosisCfg           `yaml:"diagnosis"`
	Projects  []project.Project      `yaml:"projects"`
	Source    SourceConfig           `yaml:"source"`
	Skill    SkillConfig            `yaml:"skill"`
	Feishu   FeishuCfg              `yaml:"feishu"`
	Store    StoreConfig            `yaml:"store"`
	Logger   LoggerConfig           `yaml:"logger"`
	AdminAPI AdminAPIConfig         `yaml:"admin_api"`
}

// DiagnosisCfg holds configuration for the diagnosis verification pipeline.
type DiagnosisCfg struct {
	StructuredOutput bool   `yaml:"structured_output"`
	JSONFixerEnabled bool   `yaml:"json_fixer_enabled"`
	PromptVersion    string `yaml:"prompt_version"`

	// P1: Fingerprint reuse
	FingerprintReuseEnabled  bool   `yaml:"fingerprint_reuse_enabled"`
	FingerprintReuseWindow   string `yaml:"fingerprint_reuse_window"`
	FingerprintReuseMinScore int    `yaml:"fingerprint_reuse_min_score"`
}

type AmpConfig struct {
	APIKey      string `yaml:"api_key"`
	Binary      string `yaml:"binary"`
	DefaultMode string `yaml:"default_mode"`
}

type SchedulerConfig struct {
	MaxConcurrency int    `yaml:"max_concurrency"`
	QueueSize      int    `yaml:"queue_size"`
	DefaultTimeout string `yaml:"default_timeout"`
	RetryCount     int    `yaml:"retry_count"`
	RetryDelay     string `yaml:"retry_delay"`
}

type IntakeConfig struct {
	Listen         string      `yaml:"listen"`
	Dedup          DedupConfig `yaml:"dedup"`
	RateLimit      int         `yaml:"rate_limit_per_hour"`
	MinSeverity    string      `yaml:"min_severity"`
	AuthToken      string      `yaml:"auth_token"`
	MaxPayloadSize int         `yaml:"max_payload_size"`
}

type DedupConfig struct {
	DefaultWindow string   `yaml:"default_window"`
	DefaultFields []string `yaml:"default_fields"`
}

type SourceConfig struct {
	BaseDir          string `yaml:"base_dir"`
	GitSSHKey        string `yaml:"git_ssh_key"`
	MaxCacheProjects int    `yaml:"max_cache_projects"`
}

type SkillConfig struct {
	Dir string            `yaml:"dir"`
	Env map[string]string `yaml:"env"`
}

type FeishuCfg struct {
	DefaultWebhook string `yaml:"default_webhook"`
	Timeout        string `yaml:"timeout"`
	RetryCount     int    `yaml:"retry_count"`
	SignKey        string `yaml:"sign_key"`
	DashboardURL   string `yaml:"dashboard_url"`
}

type StoreConfig struct {
	Type   string      `yaml:"type"`
	SQLite SQLiteCfg   `yaml:"sqlite"`
	MySQL  MySQLCfg    `yaml:"mysql"`
	JSON   JSONStoreCfg `yaml:"json"`
}

type SQLiteCfg struct {
	Path string `yaml:"path"`
}

type MySQLCfg struct {
	DSN             string `yaml:"dsn"`
	MaxOpenConns    int    `yaml:"max_open_conns"`
	MaxIdleConns    int    `yaml:"max_idle_conns"`
	ConnMaxLifetime string `yaml:"conn_max_lifetime"`
}

type JSONStoreCfg struct {
	Path          string `yaml:"path"`
	FlushInterval string `yaml:"flush_interval"`
}

type LoggerConfig struct {
	Level      string         `yaml:"level"`
	Console    ConsoleLogCfg  `yaml:"console"`
	File       FileLogCfg     `yaml:"file"`
	Structured StructLogCfg   `yaml:"structured"`
	Session    SessionLogCfg  `yaml:"session"`
}

type ConsoleLogCfg struct {
	Enabled bool `yaml:"enabled"`
	Color   bool `yaml:"color"`
}

type FileLogCfg struct {
	Enabled    bool   `yaml:"enabled"`
	Dir        string `yaml:"dir"`
	MaxSizeMB  int    `yaml:"max_size_mb"`
	MaxAgeDays int    `yaml:"max_age_days"`
	MaxBackups int    `yaml:"max_backups"`
}

type StructLogCfg struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

type SessionLogCfg struct {
	Enabled bool   `yaml:"enabled"`
	Dir     string `yaml:"dir"`
}

type AdminAPIConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Listen    string `yaml:"listen"`
	AuthToken string `yaml:"auth_token"`
}

// LoadConfig reads and parses the config file, expanding environment variables.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Expand ${ENV_VAR} references
	expanded := os.Expand(string(data), func(key string) string {
		return os.Getenv(key)
	})

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyDefaults()
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Amp.Binary == "" {
		c.Amp.Binary = "amp"
	}
	if c.Amp.DefaultMode == "" {
		c.Amp.DefaultMode = "smart"
	}
	if c.Scheduler.MaxConcurrency == 0 {
		c.Scheduler.MaxConcurrency = 3
	}
	if c.Scheduler.QueueSize == 0 {
		c.Scheduler.QueueSize = 100
	}
	if c.Scheduler.DefaultTimeout == "" {
		c.Scheduler.DefaultTimeout = "15m"
	}
	if c.Scheduler.RetryDelay == "" {
		c.Scheduler.RetryDelay = "10s"
	}
	if c.Intake.Listen == "" {
		c.Intake.Listen = ":8080"
	}
	if c.Intake.Dedup.DefaultWindow == "" {
		c.Intake.Dedup.DefaultWindow = "10m"
	}
	if len(c.Intake.Dedup.DefaultFields) == 0 {
		c.Intake.Dedup.DefaultFields = []string{"error_msg", "error", "message", "msg"}
	}
	if c.Intake.MaxPayloadSize == 0 {
		c.Intake.MaxPayloadSize = 65536
	}
	if c.Intake.MinSeverity == "" {
		c.Intake.MinSeverity = "warning"
	}
	if c.Source.BaseDir == "" {
		c.Source.BaseDir = "./data/repos"
	}
	if c.Store.Type == "" {
		c.Store.Type = "sqlite"
	}
	if c.Store.SQLite.Path == "" {
		c.Store.SQLite.Path = "./data/sentinel.db"
	}
	if c.Logger.Level == "" {
		c.Logger.Level = "info"
	}
	if c.Feishu.Timeout == "" {
		c.Feishu.Timeout = "10s"
	}
	if c.Feishu.RetryCount == 0 {
		c.Feishu.RetryCount = 3
	}
	if c.Logger.Session.Dir == "" {
		c.Logger.Session.Dir = "./logs/sessions"
	}
	if c.Logger.File.Enabled && c.Logger.File.Dir == "" {
		c.Logger.File.Dir = "./logs"
	}
	if c.Logger.Structured.Enabled && c.Logger.Structured.Path == "" {
		c.Logger.Structured.Path = "./logs/sentinel.ndjson"
	}
	if c.AdminAPI.Listen == "" {
		c.AdminAPI.Listen = ":8081"
	}
}

// ParseDuration parses a duration string, returning a fallback on error.
func ParseDuration(s string, fallback time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

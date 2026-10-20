package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type AppConfig struct {
	Preset string `json:"preset"`
	UA     string `json:"ua"`
	TTL    uint8  `json:"ttl"`
	Log    string `json:"log"`
}

func DefaultAppConfig() AppConfig {
	return AppConfig{
		Preset: "wechat",
		UA:     presets["wechat"],
		TTL:    64,
		Log:    "info",
	}
}

func (c AppConfig) Normalize() AppConfig {
	out := c
	if out.Preset == "" {
		out.Preset = "wechat"
	}
	out.Preset = strings.ToLower(strings.TrimSpace(out.Preset))
	if out.TTL == 0 {
		out.TTL = 64
	}
	out.Log = strings.ToLower(strings.TrimSpace(out.Log))
	if out.Log == "" {
		out.Log = "info"
	}
	switch out.Log {
	case "debug", "info", "warn":
	default:
		out.Log = "info"
	}
	if out.Preset != "pc" && out.Preset != "wechat" {
		out.Preset = "wechat"
	}
	out.UA = resolveUA(out.Preset)
	return out
}

// Validate input before normalization so invalid API values never silently pass.
// Legacy configurations may still use defaults when loaded from disk.
func (c AppConfig) Validate() error {
	if c.TTL == 0 {
		return fmt.Errorf("TTL 必须是 1 到 255 之间的整数")
	}
	switch strings.ToLower(strings.TrimSpace(c.Preset)) {
	case "", "wechat", "pc":
	default:
		return fmt.Errorf("运行模式必须为 wechat 或 pc")
	}
	switch strings.ToLower(strings.TrimSpace(c.Log)) {
	case "", "info", "debug", "warn":
	default:
		return fmt.Errorf("日志级别必须为 info、debug 或 warn")
	}
	return nil
}

func (c AppConfig) EffectiveUA() string {
	return c.Normalize().UA
}

func (c AppConfig) ToRuntimeConfig() *Config {
	norm := c.Normalize()
	return &Config{
		TTL: norm.TTL,
		UA:  norm.EffectiveUA(),
		Log: norm.Log,
	}
}

func LoadAppConfig(path string) (AppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultAppConfig(), nil
		}
		return AppConfig{}, err
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return AppConfig{}, fmt.Errorf("decode app config: %w", err)
	}
	return cfg.Normalize(), nil
}

func SaveAppConfig(path string, cfg AppConfig) error {
	cfg = cfg.Normalize()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// Keep the last complete configuration until a synced replacement is ready.
	// Unique temporary files keep independent concurrent writers isolated.
	f, err := os.CreateTemp(filepath.Dir(path), ".ua3f-config-*")
	if err != nil {
		return fmt.Errorf("prepare config: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := replaceConfigFile(tmp, path); err != nil {
		return fmt.Errorf("replace config (previous config preserved): %w", err)
	}
	return nil
}

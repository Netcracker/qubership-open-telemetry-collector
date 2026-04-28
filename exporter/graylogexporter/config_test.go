package graylogexporter

import "testing"

func TestConfigValidateAndDefaults(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should validate: %v", err)
	}
	if cfg.GELFMapping.Version != "1.1" {
		t.Fatalf("unexpected default GELF version: %q", cfg.GELFMapping.Version)
	}

	invalid := *cfg
	invalid.ConnPoolSize = 0
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected validation error for connection pool size")
	}
}

func TestParseGELFFieldMapping(t *testing.T) {
	cfg := &Config{
		GELFMapping: GELFFieldMapping{
			Version:      "1.1",
			Host:         "host",
			ShortMessage: "short",
			FullMessage:  "full",
			Level:        "level",
		},
	}
	mapping, err := parseGELFFieldMapping(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mapping.Host != "host" || mapping.ShortMessage != "short" {
		t.Fatalf("unexpected field mapping: %#v", mapping)
	}
}

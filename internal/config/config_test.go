package config

import (
	"testing"
	"time"
)

func noEnv(string) (string, bool) { return "", false }

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse(nil, noEnv)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.CRISocket != "unix:///var/run/crio/crio.sock" {
		t.Errorf("CRISocket = %q", cfg.CRISocket)
	}
	if cfg.ListenAddress != "127.0.0.1:8080" {
		t.Errorf("ListenAddress = %q", cfg.ListenAddress)
	}
	if !cfg.CollectImageAge {
		t.Error("CollectImageAge should default true")
	}
	if cfg.StorageRoot != "" {
		t.Errorf("StorageRoot should default empty, got %q", cfg.StorageRoot)
	}
	if cfg.ImageAgeRefreshInterval != 5*time.Minute {
		t.Errorf("ImageAgeRefreshInterval = %v", cfg.ImageAgeRefreshInterval)
	}
}

func TestParseFlagsOverrideDefaults(t *testing.T) {
	cfg, err := Parse([]string{"--max-images=50", "--storage-root=/var/lib/containers/storage"}, noEnv)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.MaxImages != 50 {
		t.Errorf("MaxImages = %d", cfg.MaxImages)
	}
	if cfg.StorageRoot != "/var/lib/containers/storage" {
		t.Errorf("StorageRoot = %q", cfg.StorageRoot)
	}
}

func TestParseEnvOverridesDefaultsButNotFlags(t *testing.T) {
	env := func(k string) (string, bool) {
		switch k {
		case "CRIO_IMAGE_EXPORTER_MAX_IMAGES":
			return "10", true
		case "CRIO_IMAGE_EXPORTER_LOG_LEVEL":
			return "debug", true
		}
		return "", false
	}
	cfg, err := Parse([]string{"--max-images=99"}, env)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.MaxImages != 99 {
		t.Errorf("flag must beat env: MaxImages = %d", cfg.MaxImages)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("env must beat default: LogLevel = %q", cfg.LogLevel)
	}
}

func TestParseRejectsBadImageNameFilter(t *testing.T) {
	if _, err := Parse([]string{"--image-name-filter=[unclosed"}, noEnv); err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestParseRejectsNegativeMaxImages(t *testing.T) {
	if _, err := Parse([]string{"--max-images=-1"}, noEnv); err == nil {
		t.Fatal("expected error for negative max-images")
	}
}

func TestNodeNameFromFlagAndEnv(t *testing.T) {
	cfg, err := Parse([]string{"--node-name=worker-3"}, noEnv)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.NodeName != "worker-3" {
		t.Errorf("NodeName = %q, want worker-3", cfg.NodeName)
	}

	// The DaemonSet supplies it through the downward API as an env var, which
	// is why no --node-name argument appears in the chart's container args.
	env := func(k string) (string, bool) {
		if k == "CRIO_IMAGE_EXPORTER_NODE_NAME" {
			return "worker-9", true
		}
		return "", false
	}
	cfg, err = Parse(nil, env)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.NodeName != "worker-9" {
		t.Errorf("NodeName = %q, want worker-9", cfg.NodeName)
	}

	cfg, err = Parse(nil, noEnv)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.NodeName != "" {
		t.Errorf("NodeName = %q, want empty", cfg.NodeName)
	}
}

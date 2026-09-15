// Package config defines the exporter's runtime configuration.
package config

import (
	"flag"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const envPrefix = "CRIO_IMAGE_EXPORTER_"

type Config struct {
	CRISocket     string
	ListenAddress string
	MetricsPath   string
	CRITimeout    time.Duration

	CollectImageAge        bool
	ImageAgeRefreshInterval time.Duration

	StorageRoot            string
	StorageRefreshInterval time.Duration

	ImageNameFilter string
	MaxImages       int
	DisablePerImage bool

	LogLevel string

	// ImageNameRegexp is the compiled form of ImageNameFilter, nil when empty.
	ImageNameRegexp *regexp.Regexp
}

// Parse builds a Config from command-line arguments and the environment.
// Precedence is flag > environment > default.
func Parse(args []string, lookupEnv func(string) (string, bool)) (*Config, error) {
	cfg := &Config{}
	fs := flag.NewFlagSet("crio-image-exporter", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVar(&cfg.CRISocket, "cri-socket", "unix:///var/run/crio/crio.sock", "CRI-O gRPC endpoint")
	fs.StringVar(&cfg.ListenAddress, "listen-address", "127.0.0.1:8080", "address to serve metrics on")
	fs.StringVar(&cfg.MetricsPath, "metrics-path", "/metrics", "path to serve metrics on")
	fs.DurationVar(&cfg.CRITimeout, "cri-timeout", 10*time.Second, "per-RPC timeout")
	fs.BoolVar(&cfg.CollectImageAge, "collect-image-age", true, "collect image creation timestamps")
	fs.DurationVar(&cfg.ImageAgeRefreshInterval, "image-age-refresh-interval", 5*time.Minute, "image age cache refresh interval")
	fs.StringVar(&cfg.StorageRoot, "storage-root", "", "containers/storage root for exact layer attribution; empty disables")
	fs.DurationVar(&cfg.StorageRefreshInterval, "storage-refresh-interval", 5*time.Minute, "storage graph refresh interval")
	fs.StringVar(&cfg.ImageNameFilter, "image-name-filter", "", "regex allowlist matched against repo tags; empty allows all")
	fs.IntVar(&cfg.MaxImages, "max-images", 0, "cap on per-image series; 0 is unlimited")
	fs.BoolVar(&cfg.DisablePerImage, "disable-per-image", false, "emit aggregates and health metrics only")
	fs.StringVar(&cfg.LogLevel, "log-level", "info", "log level: debug, info, warn, error")

	// Environment fills in before flags parse, so an explicit flag always wins.
	var envErr error
	fs.VisitAll(func(f *flag.Flag) {
		if envErr != nil {
			return
		}
		if v, ok := lookupEnv(envKey(f.Name)); ok {
			if err := f.Value.Set(v); err != nil {
				envErr = fmt.Errorf("env %s: %w", envKey(f.Name), err)
			}
		}
	})
	if envErr != nil {
		return nil, envErr
	}

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func envKey(flagName string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

func (c *Config) validate() error {
	if c.MaxImages < 0 {
		return fmt.Errorf("max-images must be >= 0, got %s", strconv.Itoa(c.MaxImages))
	}
	if c.CRITimeout <= 0 {
		return fmt.Errorf("cri-timeout must be positive, got %s", c.CRITimeout)
	}
	if c.ImageAgeRefreshInterval <= 0 {
		return fmt.Errorf("image-age-refresh-interval must be positive, got %s", c.ImageAgeRefreshInterval)
	}
	if c.StorageRefreshInterval <= 0 {
		return fmt.Errorf("storage-refresh-interval must be positive, got %s", c.StorageRefreshInterval)
	}
	if c.ImageNameFilter != "" {
		re, err := regexp.Compile(c.ImageNameFilter)
		if err != nil {
			return fmt.Errorf("image-name-filter: %w", err)
		}
		c.ImageNameRegexp = re
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log-level must be one of debug, info, warn, error; got %q", c.LogLevel)
	}
	return nil
}

// StorageEnabled reports whether exact layer attribution is configured.
func (c *Config) StorageEnabled() bool { return c.StorageRoot != "" }

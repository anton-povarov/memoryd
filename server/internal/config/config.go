// Package config loads and validates memoryd's process configuration.
//
// The command is responsible for selecting the path (usually with -config),
// while this package is responsible for reading that path.  Keeping those
// concerns separate makes it possible for other entry points to use the same
// validation rules.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultAddress         = "127.0.0.1:8080"
	DefaultShutdownTimeout = 10 * time.Second
	DefaultDataDir         = "./data"
	DefaultLogLevel        = "info"
)

// Config is the complete configuration needed by the server.  Omitted YAML
// values receive the values returned by Defaults.
type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Storage StorageConfig `yaml:"storage"`
	Logging LoggingConfig `yaml:"logging"`
}

type ServerConfig struct {
	Address         string        `yaml:"address"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// StorageConfig contains paths owned by the Vault.  DatabasePath and BlobDir
// are derived from DataDir when omitted.
type StorageConfig struct {
	DataDir      string `yaml:"data_dir"`
	DatabasePath string `yaml:"database_path"`
	BlobDir      string `yaml:"blob_dir"`
}

type LoggingConfig struct {
	Level string `yaml:"level"`
}

func Defaults() Config {
	c := Config{
		Server: ServerConfig{
			Address:         DefaultAddress,
			ShutdownTimeout: DefaultShutdownTimeout,
		},
		Storage: StorageConfig{DataDir: DefaultDataDir},
		Logging: LoggingConfig{Level: DefaultLogLevel},
	}
	return c.withDerivedPaths()
}

// Load reads a YAML configuration file and validates the resulting settings.
// A missing path is an error; silently falling back to defaults for a typo in
// -config would otherwise start a server with surprising settings.
func Load(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, fmt.Errorf("config path is empty")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config file %q: %w", path, err)
	}
	c, err := Parse(b)
	if err != nil {
		return Config{}, fmt.Errorf("parse config file %q: %w", path, err)
	}
	return c, nil
}

// LoadFile is an explicit synonym for Load.
func LoadFile(path string) (Config, error) { return Load(path) }

// Parse decodes and validates YAML configuration bytes without involving the
// filesystem. Load should be preferred by command entry points because it
// preserves the explicit missing-file error.
func Parse(data []byte) (Config, error) {
	c, err := parseYAMLConfig(data)
	if err != nil {
		return Config{}, err
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate checks security-sensitive and otherwise process-wide settings.
func (c Config) Validate() error {
	if err := validateAddress(c.Server.Address); err != nil {
		return fmt.Errorf("server.address: %w", err)
	}
	if c.Server.ShutdownTimeout <= 0 {
		return fmt.Errorf("server.shutdown_timeout must be greater than zero")
	}

	if _, err := ParseLoggingLevel(c.Logging.Level); err != nil {
		return err
	}
	if strings.TrimSpace(c.Storage.DataDir) == "" {
		return fmt.Errorf("storage.data_dir must not be empty")
	}
	return nil
}

// ParseLoggingLevel validates a configured level and returns its canonical
// name. Both "warn" and "warning" map to "warn".
func ParseLoggingLevel(value string) (string, error) {
	switch level := strings.ToLower(strings.TrimSpace(value)); level {
	case "debug", "info", "error":
		return level, nil
	case "warn", "warning":
		return "warn", nil
	default:
		return "", fmt.Errorf("logging.level must be one of debug, info, warn, warning, or error (got %q)", value)
	}
}

func (c Config) withDerivedPaths() Config {
	if c.Storage.DatabasePath == "" {
		c.Storage.DatabasePath = filepath.Join(c.Storage.DataDir, "memoryd.sqlite")
	}
	if c.Storage.BlobDir == "" {
		c.Storage.BlobDir = filepath.Join(c.Storage.DataDir, "blobs")
	}
	return c
}

func validateAddress(address string) error {
	address = strings.TrimSpace(address)
	if address == "" {
		return fmt.Errorf("must not be empty")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("must be host:port: %w", err)
	}
	if host == "" {
		return fmt.Errorf("must bind to a loopback host, not all interfaces")
	}
	if port == "" {
		return fmt.Errorf("must include a port")
	}
	if strings.EqualFold(host, "localhost") {
		return validatePort(port)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("host %q is not a loopback address", host)
	}
	return validatePort(port)
}

func validatePort(port string) error {
	var n int
	if _, err := fmt.Sscanf(port, "%d", &n); err != nil || n < 1 || n > 65535 || fmt.Sprintf("%d", n) != port {
		return fmt.Errorf("port %q is not in the range 1..65535", port)
	}
	return nil
}

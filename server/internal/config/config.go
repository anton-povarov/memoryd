// Package config loads and validates memoryd's process configuration.
//
// The command is responsible for selecting the path (usually with -config),
// while this package is responsible for reading that path.  Keeping those
// concerns separate makes it possible for other entry points to use the same
// validation rules.
package config

import (
	"fmt"
	"log/slog"
	"mime"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/anton-povarov/memoryd/server/internal/understanding"
)

const (
	DefaultAddress                    = "127.0.0.1:8080"
	DefaultShutdownTimeout            = 10 * time.Second
	DefaultDataDir                    = "./data"
	DefaultLogLevel                   = slog.LevelInfo
	DefaultUnderstandingMaxConcurrent = 1
)

// Config is the complete configuration needed by the server.  Omitted YAML
// values receive the values returned by Defaults.
type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Storage       StorageConfig       `yaml:"storage"`
	Logging       LoggingConfig       `yaml:"logging"`
	Understanding UnderstandingConfig `yaml:"understanding"`
	Models        ModelsConfig        `yaml:"models"`
}

type ModelsConfig struct {
	DocumentUnderstanding *understanding.RequestModel `yaml:"document_understanding"`
}

type ServerConfig struct {
	Address         string        `yaml:"address"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// StorageConfig contains paths owned by the Vault. DatabasePath, BlobDir, and
// UploadDir are derived from DataDir when omitted.
type StorageConfig struct {
	DataDir      string `yaml:"data_dir"`
	DatabasePath string `yaml:"database_path"`
	BlobDir      string `yaml:"blob_dir"`
	UploadDir    string `yaml:"upload_dir"`
}

type LoggingConfig struct {
	Level slog.Level `yaml:"level"`
}

// UnderstandingConfig configures detached workers and their trusted plugins.
type UnderstandingConfig struct {
	MaxConcurrent int                                  `yaml:"max_concurrent"`
	Plugins       map[string]UnderstandingPluginConfig `yaml:"plugins"`
}

type UnderstandingPluginConfig struct {
	Command    []string `yaml:"command"`
	MediaTypes []string `yaml:"media_types"`
	LogDir     string   `yaml:"log_dir"`
}

func Defaults() Config {
	c := Config{
		Server: ServerConfig{
			Address:         DefaultAddress,
			ShutdownTimeout: DefaultShutdownTimeout,
		},
		Storage: StorageConfig{
			DataDir:      DefaultDataDir,
			DatabasePath: "",
			BlobDir:      "",
			UploadDir:    "",
		},
		Logging: LoggingConfig{Level: DefaultLogLevel},
		Understanding: UnderstandingConfig{
			MaxConcurrent: DefaultUnderstandingMaxConcurrent,
			Plugins:       nil,
		},
		Models: ModelsConfig{DocumentUnderstanding: nil},
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
	if strings.TrimSpace(c.Storage.DataDir) == "" {
		return fmt.Errorf("storage.data_dir must not be empty")
	}
	if err := validateUnderstandingConfig(c.Understanding); err != nil {
		return fmt.Errorf("understanding: %w", err)
	}
	if model := c.Models.DocumentUnderstanding; model != nil {
		if err := model.Validate(); err != nil {
			return fmt.Errorf("models.document_understanding: %w", err)
		}
	}
	return nil
}

func validateUnderstandingConfig(understanding UnderstandingConfig) error {
	if understanding.MaxConcurrent < 1 {
		return fmt.Errorf("max_concurrent must be greater than zero")
	}
	pluginIDs := make([]string, 0, len(understanding.Plugins))
	for pluginID := range understanding.Plugins {
		pluginIDs = append(pluginIDs, pluginID)
	}
	sort.Strings(pluginIDs)

	configuredMediaTypes := make(map[string]string)
	for _, pluginID := range pluginIDs {
		if strings.TrimSpace(pluginID) == "" || strings.TrimSpace(pluginID) != pluginID {
			return fmt.Errorf("plugin ID must be nonempty and have no surrounding whitespace")
		}

		plugin := understanding.Plugins[pluginID]
		if len(plugin.Command) == 0 || strings.TrimSpace(plugin.Command[0]) == "" {
			return fmt.Errorf(
				"plugin %q command must contain a nonempty executable",
				pluginID,
			)
		}
		if len(plugin.MediaTypes) == 0 {
			return fmt.Errorf("plugin %q must declare at least one Media Type", pluginID)
		}

		for _, mediaType := range plugin.MediaTypes {
			parsedMediaType, parameters, err := mime.ParseMediaType(mediaType)
			if err != nil || parsedMediaType != mediaType || len(parameters) != 0 ||
				strings.Count(parsedMediaType, "/") != 1 ||
				strings.Contains(mediaType, "*") {
				return fmt.Errorf(
					"plugin %q has invalid canonical Media Type %q",
					pluginID,
					mediaType,
				)
			}
			if previousPlugin, exists := configuredMediaTypes[mediaType]; exists {
				return fmt.Errorf(
					"media type %q is configured by both plugins %q and %q",
					mediaType,
					previousPlugin,
					pluginID,
				)
			}
			configuredMediaTypes[mediaType] = pluginID
		}
	}
	return nil
}

func (c Config) withDerivedPaths() Config {
	if c.Storage.DatabasePath == "" {
		c.Storage.DatabasePath = filepath.Join(
			c.Storage.DataDir,
			"memoryd.sqlite",
		)
	}
	if c.Storage.BlobDir == "" {
		c.Storage.BlobDir = filepath.Join(c.Storage.DataDir, "blobs")
	}
	if c.Storage.UploadDir == "" {
		c.Storage.UploadDir = filepath.Join(c.Storage.DataDir, "uploads")
	}
	return c
}

func validateAddress(address string) error {
	address = strings.TrimSpace(address)
	if address == "" {
		return fmt.Errorf("must not be empty")
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("must be host:port: %w", err)
	}
	if port == "" {
		return fmt.Errorf("must include a port")
	}
	return nil
}

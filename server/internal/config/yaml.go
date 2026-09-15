package config

import (
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// rawConfig keeps duration as text because YAML has no native duration type;
// it is converted with time.ParseDuration after strict decoding.
type rawConfig struct {
	Server  *rawServer  `yaml:"server"`
	Storage *rawStorage `yaml:"storage"`
	Logging *rawLogging `yaml:"logging"`
}

type rawServer struct {
	Address         *string `yaml:"address"`
	ShutdownTimeout *string `yaml:"shutdown_timeout"`
}

type rawStorage struct {
	DataDir      *string `yaml:"data_dir"`
	DatabasePath *string `yaml:"database_path"`
	BlobDir      *string `yaml:"blob_dir"`
}

type rawLogging struct {
	Level *string `yaml:"level"`
}

func parseYAMLConfig(data []byte) (Config, error) {
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	var raw rawConfig
	if err := decoder.Decode(&raw); err != nil {
		if err == io.EOF {
			return Defaults(), nil
		}
		return Config{}, err
	}
	// A config file is one YAML document. Reject a second document rather than
	// silently ignoring it.
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf(
				"multiple YAML documents are not supported",
			)
		}
		return Config{}, err
	}

	c := Defaults()
	c.Storage.DatabasePath = ""
	c.Storage.BlobDir = ""
	if raw.Server != nil {
		if raw.Server.Address != nil {
			c.Server.Address = *raw.Server.Address
		}
		if raw.Server.ShutdownTimeout != nil {
			duration, err := time.ParseDuration(
				strings.TrimSpace(*raw.Server.ShutdownTimeout),
			)
			if err != nil {
				return Config{}, fmt.Errorf(
					"server.shutdown_timeout must be a duration such as 10s: %w",
					err,
				)
			}
			c.Server.ShutdownTimeout = duration
		}
	}
	if raw.Storage != nil {
		if raw.Storage.DataDir != nil {
			c.Storage.DataDir = *raw.Storage.DataDir
		}
		if raw.Storage.DatabasePath != nil {
			c.Storage.DatabasePath = *raw.Storage.DatabasePath
		}
		if raw.Storage.BlobDir != nil {
			c.Storage.BlobDir = *raw.Storage.BlobDir
		}
	}
	if raw.Logging != nil {
		if raw.Logging.Level != nil {
			c.Logging.Level = *raw.Logging.Level
		}
	}
	return c.withDerivedPaths(), nil
}

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsAreSafe(t *testing.T) {
	c := Defaults()
	if c.Server.Address != "127.0.0.1:8080" {
		t.Fatalf("default address = %q", c.Server.Address)
	}
	if c.Server.ShutdownTimeout != 10*time.Second {
		t.Fatalf("default shutdown timeout = %s", c.Server.ShutdownTimeout)
	}
	if c.Logging.Level != "info" || c.Logging.Format != "text" {
		t.Fatalf("default logging = %#v", c.Logging)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
}

func TestLoadAppliesValuesAndDerivedPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memoryd.yaml")
	contents := `server:
  address: "[::1]:9090"
  shutdown_timeout: 25s
storage:
  data_dir: ./vault
logging:
  level: DEBUG
  format: json
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Address != "[::1]:9090" || c.Server.ShutdownTimeout != 25*time.Second {
		t.Fatalf("server config = %#v", c.Server)
	}
	if c.Storage.DatabasePath != filepath.Join("vault", "memoryd.sqlite") || c.Storage.BlobDir != filepath.Join("vault", "blobs") {
		t.Fatalf("derived storage paths = %#v", c.Storage)
	}
	if c.Logging != (LoggingConfig{Level: "DEBUG", Format: "json"}) {
		t.Fatalf("logging config = %#v", c.Logging)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memoryd.yaml")
	if err := os.WriteFile(path, []byte("server:\n  address: 127.0.0.1:8080\n  typo: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "field typo") {
		t.Fatalf("unknown field error = %v", err)
	}
}

func TestLoadMissingFileIsExplicit(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error = %v", err)
	}
}

func TestValidateRejectsNonLoopbackAddress(t *testing.T) {
	c := Defaults()
	c.Server.Address = "0.0.0.0:8080"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback validation error = %v", err)
	}
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memoryd.yaml")
	if err := os.WriteFile(path, []byte("server:\n  shutdown_timeout: soon\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "shutdown_timeout") {
		t.Fatalf("invalid duration error = %v", err)
	}
}

func TestParseLoggingLevel(t *testing.T) {
	for _, value := range []string{"warn", "warning", "WARNING"} {
		level, err := ParseLoggingLevel(value)
		if err != nil || level != "warn" {
			t.Errorf("ParseLoggingLevel(%q) = %q, %v", value, level, err)
		}
	}
	_, err := ParseLoggingLevel("trace")
	if err == nil || !strings.Contains(err.Error(), "warning") {
		t.Fatalf("invalid level error = %v", err)
	}
}

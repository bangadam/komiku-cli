package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bangadam/komiku-cli/store"
)

const (
	DefaultOutput = "."
	DefaultDelay  = 200 * time.Millisecond
	DefaultPreset = "medium"
)

type FileConfig struct {
	OutputRoot string `json:"output_root,omitempty"`
	ImageDelay string `json:"image_delay,omitempty"`
	Preset     string `json:"preset,omitempty"`
}

type Overrides struct {
	OutputRoot *string
	ImageDelay *time.Duration
	Preset     *string
}

type Config struct {
	OutputRoot string
	ImageDelay time.Duration
	Preset     string
}

func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "komiku-cli", "config.json"), nil
}

func LoadFileConfig(filename string) (FileConfig, error) {
	data, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return FileConfig{}, nil
	}
	if err != nil {
		return FileConfig{}, fmt.Errorf("read config: %w", err)
	}
	var config FileConfig
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		// Configs written before the KOReader upload feature was removed
		// still carry a "kindle" block. Drop it and retry so an old file
		// never bricks every command.
		if !strings.Contains(err.Error(), `unknown field "kindle"`) {
			return FileConfig{}, fmt.Errorf("invalid config JSON: %w", err)
		}
		migrated, migrateErr := stripLegacyKeys(data, "kindle")
		if migrateErr != nil {
			return FileConfig{}, fmt.Errorf("invalid config JSON: %w", err)
		}
		config = FileConfig{}
		decoder = json.NewDecoder(strings.NewReader(string(migrated)))
		decoder.DisallowUnknownFields()
		if retryErr := decoder.Decode(&config); retryErr != nil {
			return FileConfig{}, fmt.Errorf("invalid config JSON: %w", retryErr)
		}
		if saveErr := SaveFileConfig(filename, config); saveErr != nil {
			return FileConfig{}, fmt.Errorf("migrate config: %w", saveErr)
		}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return FileConfig{}, fmt.Errorf("invalid trailing config JSON: %w", err)
	}
	return config, nil
}

// stripLegacyKeys removes keys that no longer exist in FileConfig.
func stripLegacyKeys(data []byte, keys ...string) ([]byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	for _, key := range keys {
		delete(raw, key)
	}
	return json.Marshal(raw)
}

func SaveFileConfig(filename string, config FileConfig) error {
	if _, err := ResolveConfig(config, Overrides{}); err != nil {
		return err
	}
	return store.WriteJSONAtomic(filename, config)
}

func ResolveConfig(file FileConfig, flags Overrides) (Config, error) {
	config := Config{OutputRoot: DefaultOutput, ImageDelay: DefaultDelay, Preset: DefaultPreset}
	if file.OutputRoot != "" {
		config.OutputRoot = file.OutputRoot
	}
	if file.ImageDelay != "" {
		delay, err := time.ParseDuration(file.ImageDelay)
		if err != nil {
			return Config{}, fmt.Errorf("invalid config image_delay %q: %w", file.ImageDelay, err)
		}
		config.ImageDelay = delay
	}
	if file.Preset != "" {
		config.Preset = file.Preset
	}
	if flags.OutputRoot != nil {
		config.OutputRoot = *flags.OutputRoot
	}
	if flags.ImageDelay != nil {
		config.ImageDelay = *flags.ImageDelay
	}
	if flags.Preset != nil {
		config.Preset = *flags.Preset
	}
	if config.OutputRoot == "" || strings.TrimSpace(config.OutputRoot) == "" || strings.IndexByte(config.OutputRoot, 0) >= 0 {
		return Config{}, errors.New("output root is invalid")
	}
	if config.ImageDelay < 0 {
		return Config{}, errors.New("image delay must not be negative")
	}
	switch config.Preset {
	case "medium", "small", "tiny", "raw":
	default:
		return Config{}, fmt.Errorf("unknown preset %q; expected medium, small, tiny, or raw", config.Preset)
	}
	return config, nil
}

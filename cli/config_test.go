package cli

import (
	"testing"
	"time"
)

func TestResolveConfigPrecedenceAndValidation(t *testing.T) {
	config, err := ResolveConfig(FileConfig{}, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if config.OutputRoot != "." || config.ImageDelay != 200*time.Millisecond || config.Preset != "medium" {
		t.Fatalf("defaults = %#v", config)
	}
	config, err = ResolveConfig(FileConfig{OutputRoot: "/config", ImageDelay: "1s", Preset: "small"}, Overrides{
		OutputRoot: pointer("/flag"), ImageDelay: durationPointer(50 * time.Millisecond), Preset: pointer("raw"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.OutputRoot != "/flag" || config.ImageDelay != 50*time.Millisecond || config.Preset != "raw" {
		t.Fatalf("flag precedence = %#v", config)
	}
	for name, file := range map[string]FileConfig{
		"negative": {ImageDelay: "-1s"},
		"preset":   {Preset: "huge"},
		"output":   {OutputRoot: "\x00"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolveConfig(file, Overrides{}); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func pointer(value string) *string                       { return &value }
func durationPointer(value time.Duration) *time.Duration { return &value }

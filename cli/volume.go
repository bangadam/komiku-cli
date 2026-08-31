package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bangadam/komiku-cli/komiku"
	"github.com/bangadam/komiku-cli/store"
)

func ParseManualVolumeMapping(expression string, maxChapter int) ([]komiku.Volume, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return nil, fmt.Errorf("manual mapping is empty; use 1:1-7,2:8-15")
	}
	volumes := make([]komiku.Volume, 0)
	for _, rawEntry := range strings.Split(expression, ",") {
		entry := strings.TrimSpace(rawEntry)
		parts := strings.Split(entry, ":")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid mapping %q; use volume:start-end", entry)
		}
		volume, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, fmt.Errorf("invalid volume number in %q", entry)
		}
		rangeParts := strings.Split(strings.TrimSpace(parts[1]), "-")
		if len(rangeParts) != 2 {
			return nil, fmt.Errorf("invalid chapter range in %q", entry)
		}
		start, startErr := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
		end, endErr := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
		if startErr != nil || endErr != nil {
			return nil, fmt.Errorf("invalid chapter range in %q", entry)
		}
		volumes = append(volumes, komiku.Volume{Volume: volume, Start: start, End: end})
	}
	if err := komiku.ValidateVolumes(volumes, maxChapter); err != nil {
		return nil, err
	}
	return volumes, nil
}

func SaveManualVolumeMapping(seriesDir string, volumes []komiku.Volume, maxChapter int) error {
	if err := komiku.ValidateVolumes(volumes, maxChapter); err != nil {
		return err
	}
	cache := komiku.VolumeCache{Source: "manual-tui", Volumes: volumes}
	if err := store.WriteJSONAtomic(filepath.Join(seriesDir, ".volumes.json"), cache); err != nil {
		return fmt.Errorf("write manual volume mapping: %w", err)
	}
	return nil
}

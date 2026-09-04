package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bangadam/komiku-cli/store"
)

// Subscription records a series whose new chapters komiku-cli should track.
type Subscription struct {
	SeriesURL string    `json:"series_url"`
	Slug      string    `json:"slug"`
	AddedAt   time.Time `json:"added_at"`
	LastCheck time.Time `json:"last_check,omitempty"`
}

// SubscriptionsFile is the persisted subscription list.
type SubscriptionsFile struct {
	Subscriptions []Subscription `json:"subscriptions"`
}

// SubscriptionsPath returns the path to the persisted subscription list,
// colocated with the config file in the user config directory.
func SubscriptionsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "komiku-cli", "subscriptions.json"), nil
}

// LoadSubscriptions reads the persisted subscription list. A missing file is
// not an error; it yields an empty list.
func LoadSubscriptions(path string) (SubscriptionsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SubscriptionsFile{}, nil
		}
		return SubscriptionsFile{}, fmt.Errorf("read subscriptions: %w", err)
	}
	var file SubscriptionsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return SubscriptionsFile{}, fmt.Errorf("decode subscriptions: %w", err)
	}
	return file, nil
}

// SaveSubscriptions atomically writes the subscription list.
func SaveSubscriptions(path string, file SubscriptionsFile) error {
	sort.SliceStable(file.Subscriptions, func(i, j int) bool {
		return file.Subscriptions[i].Slug < file.Subscriptions[j].Slug
	})
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create subscriptions directory: %w", err)
		}
	}
	return writeSubsJSON(path, file)
}

// package's crash-durable helper. It is a var so tests can capture writes.
var writeSubsJSON = func(path string, value any) error {
	return store.WriteJSONAtomic(path, value)
}

// AddSubscription appends a series to the subscription list, deduplicating by
// series URL. It returns the updated file and whether a new entry was added.
func AddSubscription(file SubscriptionsFile, seriesURL, slug string, now time.Time) (SubscriptionsFile, bool) {
	for _, sub := range file.Subscriptions {
		if sub.SeriesURL == seriesURL {
			return file, false
		}
	}
	file.Subscriptions = append(file.Subscriptions, Subscription{
		SeriesURL: seriesURL,
		Slug:      slug,
		AddedAt:   now,
	})
	return file, true
}

// RemoveSubscription drops a series from the subscription list by slug or URL.
// It returns the updated file and whether a match was found.
func RemoveSubscription(file SubscriptionsFile, identifier string) (SubscriptionsFile, bool) {
	identifier = strings.TrimSpace(identifier)
	for i, sub := range file.Subscriptions {
		if sub.Slug == identifier || sub.SeriesURL == identifier {
			file.Subscriptions = append(file.Subscriptions[:i], file.Subscriptions[i+1:]...)
			return file, true
		}
	}
	return file, false
}

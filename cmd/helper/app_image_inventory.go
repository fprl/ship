package helper

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/utils"
)

type imageRelease struct {
	Release      string
	Image        string
	ImageID      string
	ShipTags     []string
	Envelope     envelope.Envelope
	EnvelopeHash string
	CreatedAt    time.Time
}

type imageEntry struct {
	ID         string            `json:"Id"`
	Repository string            `json:"Repository"`
	Tag        string            `json:"Tag"`
	Names      []string          `json:"Names"`
	Labels     map[string]string `json:"Labels"`
	RepoTags   []string          `json:"RepoTags"`
	CreatedAt  string            `json:"CreatedAt"`
	Config     struct {
		Labels  map[string]string `json:"Labels"`
		Created string            `json:"Created"`
	} `json:"Config"`
}

func podmanAllImagesForEnv(app, env string) ([]imageRelease, error) {
	out, err := utils.RunChecked("podman", []string{"images", "--format", "json"}, "")
	if err != nil {
		return nil, err
	}
	data := []byte(strings.TrimSpace(string(out)))
	if len(data) == 0 {
		return nil, fmt.Errorf("podman images returned empty output")
	}
	var entries []imageEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse podman images json: %v", err)
	}
	return imageReleasesFromEntries(app, env, entries), nil
}

func podmanImages(app, env string) ([]imageRelease, error) {
	return podmanAllImagesForEnv(app, env)
}

func imageReleasesFromEntries(app, env string, entries []imageEntry) []imageRelease {
	out := make([]imageRelease, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		labels := entry.Labels
		if labels == nil {
			labels = entry.Config.Labels
		}
		if labels["ship.app"] != app || labels["ship.env"] != env || entry.ID == "" {
			continue
		}
		release := labels["ship.release"]
		if release == "" || release == "<none>" {
			continue
		}
		key := normalizeImageID(entry.ID)
		if seen[key] {
			continue
		}
		seen[key] = true
		decoded, decodeErr := envelope.DecodeLabel(labels[envelope.Label])
		envelopeHash := ""
		if decodeErr == nil {
			label, labelErr := decoded.LabelValue()
			if labelErr == nil {
				envelopeHash = envelope.HashLabel(label)
			}
		}
		created := entry.CreatedAt
		if created == "" {
			created = entry.Config.Created
		}
		// Podman canonicalizes unqualified build tags to localhost/<name>;
		// ship-owned tags are recognized by suffix so real and bare forms
		// both match, and every alias is collected so GC can untag them all
		// before removing the image by ID (podman refuses ID removal while
		// any tag remains).
		repoPrefix := identity.ImageRepo(app, env) + ":"
		var shipTags []string
		seenTags := map[string]bool{}
		for _, name := range append(append([]string{}, entry.Names...), entry.RepoTags...) {
			if seenTags[name] {
				continue
			}
			if strings.HasPrefix(strings.TrimPrefix(name, "localhost/"), repoPrefix) {
				seenTags[name] = true
				shipTags = append(shipTags, name)
			}
		}
		out = append(out, imageRelease{Release: release, Image: entry.ID, ImageID: entry.ID, ShipTags: shipTags, Envelope: decoded, EnvelopeHash: envelopeHash, CreatedAt: parseImageCreatedAt(created)})
	}
	return out
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func parseImageCreatedAt(value string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05 -0700 MST", "2006-01-02 15:04:05 -0700"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

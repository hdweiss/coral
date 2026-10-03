// Package config persists user state such as pinned clusters and namespaces.
package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Pin is a favorite cluster (Namespace == "") or namespace of a cluster.
type Pin struct {
	Context   string `json:"context"`
	Namespace string `json:"namespace,omitempty"`
}

type pinsFile struct {
	Pins []Pin `json:"pins"`
}

// PinsPath returns where pins are stored. Demo mode uses its own file so the
// fake clusters never show up in the real list.
func PinsPath(demo bool) string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	name := "pins.json"
	if demo {
		name = "pins-demo.json"
	}
	return filepath.Join(dir, "coralctl", name)
}

// LoadPins reads the pins at path. A missing file is no pins.
func LoadPins(path string) ([]Pin, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f pinsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Pins, nil
}

// SavePins writes pins to path atomically. An empty path saves nothing.
func SavePins(path string, pins []Pin) error {
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(pinsFile{Pins: pins}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

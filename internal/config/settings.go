package config

import (
	"errors"
	"io/fs"
	"os"
	"path"

	"sigs.k8s.io/yaml"
)

// Settings is the user's hand-written configuration, config.yaml:
//
//	# Contexts where coral refuses to change anything (shell globs).
//	readonly:
//	  - "*prod*"
type Settings struct {
	ReadOnly []string `json:"readonly,omitempty"`
}

// SettingsPath returns where config.yaml lives.
func SettingsPath() string { return Path("config.yaml") }

// LoadSettings reads config.yaml at path. A missing file or empty path is
// the defaults.
func LoadSettings(p string) (Settings, error) {
	var s Settings
	if p == "" {
		return s, nil
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := yaml.UnmarshalStrict(b, &s); err != nil {
		return s, err
	}
	for _, pat := range s.ReadOnly {
		if _, err := path.Match(pat, ""); err != nil {
			return s, errors.New("readonly: bad pattern " + pat)
		}
	}
	return s, nil
}

// IsReadOnly reports whether a context matches one of the readonly patterns.
func (s Settings) IsReadOnly(context string) bool {
	for _, pat := range s.ReadOnly {
		if ok, _ := path.Match(pat, context); ok {
			return true
		}
	}
	return false
}

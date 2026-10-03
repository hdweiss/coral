// Package theme defines coralctl's color palette and loads it from Omarchy
// themes (https://omarchy.org), whose colors.toml names every color by role.
package theme

import (
	"bufio"
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Theme holds the colors of the UI as "#rrggbb" strings.
type Theme struct {
	Accent  string // focused borders, titles, keys in the status bar
	Fg      string // selected and bar text
	Muted   string // secondary text, headers
	Border  string // unfocused borders
	SelBg   string // selection in the focused box
	SelBgLo string // selection in unfocused boxes
	BarBg   string // header and status bar
	LogoFg  string // text on the accent-colored logo
	Green   string
	Yellow  string
	Red     string
	Blue    string
	Cyan    string
	Purple  string
	Orange  string
}

// Builtin holds the palettes selectable by name with --theme.
var Builtin = map[string]Theme{
	"coral": {
		Accent:  "#FF7F50",
		Fg:      "#D8DEE9",
		Muted:   "#7B8494",
		Border:  "#3B4252",
		SelBg:   "#3B4252",
		SelBgLo: "#2A303B",
		BarBg:   "#232831",
		LogoFg:  "#1E222A",
		Green:   "#A3BE8C",
		Yellow:  "#EBCB8B",
		Red:     "#BF616A",
		Blue:    "#81A1C1",
		Cyan:    "#88C0D0",
		Purple:  "#B48EAD",
		Orange:  "#D08770",
	},
	// Kanagawa Wave, after https://github.com/rebelot/kanagawa.nvim.
	"kanagawa": {
		Accent:  "#7E9CD8", // crystalBlue
		Fg:      "#DCD7BA", // fujiWhite
		Muted:   "#727169", // fujiGray
		Border:  "#54546D", // sumiInk6
		SelBg:   "#2D4F67", // waveBlue2
		SelBgLo: "#223249", // waveBlue1
		BarBg:   "#16161D", // sumiInk0
		LogoFg:  "#1F1F28", // sumiInk3
		Green:   "#98BB6C", // springGreen
		Yellow:  "#E6C384", // carpYellow
		Red:     "#E46876", // waveRed
		Blue:    "#7FB4CA", // springBlue
		Cyan:    "#7AA89F", // waveAqua2
		Purple:  "#957FB8", // oniViolet
		Orange:  "#FFA066", // surimiOrange
	},
}

// Default is the built-in coral palette.
func Default() Theme { return Builtin["coral"] }

// Names returns the names of the built-in themes, sorted.
func Names() []string { return slices.Sorted(maps.Keys(Builtin)) }

// omarchyKeys maps colors.toml keys to the theme colors they set.
var omarchyKeys = map[string]func(*Theme) *string{
	"accent":             func(t *Theme) *string { return &t.Accent },
	"foreground":         func(t *Theme) *string { return &t.Fg },
	"dark_foreground":    func(t *Theme) *string { return &t.Muted },
	"selection":          func(t *Theme) *string { return &t.SelBg },
	"lighter_background": func(t *Theme) *string { return &t.SelBgLo },
	"dark_background":    func(t *Theme) *string { return &t.BarBg },
	"background":         func(t *Theme) *string { return &t.LogoFg },
	"green":              func(t *Theme) *string { return &t.Green },
	"yellow":             func(t *Theme) *string { return &t.Yellow },
	"red":                func(t *Theme) *string { return &t.Red },
	"blue":               func(t *Theme) *string { return &t.Blue },
	"cyan":               func(t *Theme) *string { return &t.Cyan },
	"magenta":            func(t *Theme) *string { return &t.Purple },
	"orange":             func(t *Theme) *string { return &t.Orange },
}

// ParseOmarchy reads an Omarchy colors.toml. Colors it does not set, or sets
// to something other than a hex color, keep their default. The format is flat
// `key = "#rrggbb"` lines, so it is parsed without a TOML library.
func ParseOmarchy(data []byte) Theme {
	t := Default()
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == '[' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, `"`) {
			q, _, ok := strings.Cut(v[1:], `"`)
			if !ok {
				continue
			}
			v = q
		}
		field, known := omarchyKeys[k]
		if !known || !isHex(v) {
			continue
		}
		*field(&t) = v
		if k == "selection" {
			t.Border = v // Omarchy has no border color; selection is the closest
		}
	}
	return t
}

func isHex(s string) bool {
	if (len(s) != 4 && len(s) != 7) || s[0] != '#' {
		return false
	}
	_, err := strconv.ParseUint(s[1:], 16, 32)
	return err == nil
}

// OmarchyPath returns the colors.toml of the active Omarchy theme, or "" if
// Omarchy is not installed. Current Omarchy keeps the theme under
// ~/.local/state; older versions symlinked it under ~/.config.
func OmarchyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, dir := range []string{
		filepath.Join(home, ".local", "state", "omarchy", "current", "theme"),
		filepath.Join(home, ".config", "omarchy", "current", "theme"),
	} {
		p := filepath.Join(dir, "colors.toml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Source is where the theme comes from, as given to --theme.
type Source struct {
	path  string // colors.toml to load; "" means fixed
	fixed Theme  // the theme when there is no file
	last  []byte // contents last loaded, to detect changes
}

// Resolve turns a --theme value into a Source: "auto" uses the Omarchy theme
// when there is one and coral otherwise, a Builtin name uses that palette,
// "omarchy" requires Omarchy, and anything else is a path to a colors.toml or
// a theme directory.
func Resolve(spec string) (*Source, error) {
	if t, ok := Builtin[spec]; ok {
		return &Source{fixed: t}, nil
	}
	switch spec {
	case "", "auto":
		return &Source{path: OmarchyPath(), fixed: Default()}, nil
	case "omarchy":
		p := OmarchyPath()
		if p == "" {
			return nil, fmt.Errorf("no Omarchy theme found")
		}
		return &Source{path: p}, nil
	}
	if !strings.ContainsRune(spec, filepath.Separator) {
		if _, err := os.Stat(spec); err != nil {
			return nil, fmt.Errorf("unknown theme %q: use auto, omarchy, %s, or a path to a colors.toml",
				spec, strings.Join(Names(), ", "))
		}
	}
	return &Source{path: spec}, nil
}

// Load returns the theme of the source.
func (s *Source) Load() (Theme, error) {
	if s.path == "" {
		return s.fixed, nil
	}
	if fi, err := os.Stat(s.path); err == nil && fi.IsDir() {
		s.path = filepath.Join(s.path, "colors.toml")
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return Theme{}, err
	}
	s.last = b
	return ParseOmarchy(b), nil
}

// Changed reloads the theme if its file changed since the last load, such as
// after omarchy-theme-set. It reports false while the file is unchanged or
// missing, as it briefly is while a theme switch replaces it.
func (s *Source) Changed() (Theme, bool) {
	if s == nil || s.path == "" {
		return Theme{}, false
	}
	b, err := os.ReadFile(s.path)
	if err != nil || bytes.Equal(b, s.last) {
		return Theme{}, false
	}
	s.last = b
	return ParseOmarchy(b), true
}

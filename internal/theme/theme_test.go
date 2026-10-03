package theme

import (
	"os"
	"path/filepath"
	"testing"
)

const tokyoNight = `mode = "dark"

accent = "#7aa2f7"
selection = "#292e42"
muted = "#414868"

background = "#1a1b26"
dark_background = "#13141c"
lighter_background = "#24283b"

foreground = "#a9b1d6"
dark_foreground = "#565f89"

red = "#f7768e"
yellow = "#e0af68"
orange = "#eb927b"
green = "#9ece6a"
cyan = "#449dab"
blue = "#7aa2f7"
magenta = "#ad8ee6"
bright_red = "#ff7a93"
`

func TestParseOmarchy(t *testing.T) {
	got := ParseOmarchy([]byte(tokyoNight))
	want := Theme{
		Accent: "#7aa2f7", Fg: "#a9b1d6", Muted: "#565f89", Border: "#292e42",
		SelBg: "#292e42", SelBgLo: "#24283b", BarBg: "#13141c", LogoFg: "#1a1b26",
		Green: "#9ece6a", Yellow: "#e0af68", Red: "#f7768e", Blue: "#7aa2f7",
		Cyan: "#449dab", Purple: "#ad8ee6", Orange: "#eb927b",
	}
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseOmarchyPartial(t *testing.T) {
	got := ParseOmarchy([]byte(`# comment
accent = "#123456" # trailing comment
red = "rgba(1,2,3,0.5)"
green = '#abcdef'
not a pair
blue = "#abc
`))
	want := Default()
	want.Accent = "#123456"
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func writeColors(t *testing.T, dir, data string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "colors.toml")
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if p := OmarchyPath(); p != "" {
		t.Fatalf("OmarchyPath without Omarchy = %q", p)
	}
	if _, err := Resolve("omarchy"); err == nil {
		t.Error(`Resolve("omarchy") without Omarchy succeeded`)
	}
	s, _ := Resolve("auto")
	if th, err := s.Load(); err != nil || th != Default() {
		t.Errorf("auto without Omarchy = %+v, %v", th, err)
	}

	// The legacy location is used when it is the only one.
	legacy := writeColors(t, filepath.Join(home, ".config", "omarchy", "current", "theme"), `accent = "#000001"`)
	if p := OmarchyPath(); p != legacy {
		t.Errorf("OmarchyPath = %q, want %q", p, legacy)
	}
	state := writeColors(t, filepath.Join(home, ".local", "state", "omarchy", "current", "theme"), `accent = "#000002"`)
	if p := OmarchyPath(); p != state {
		t.Errorf("OmarchyPath = %q, want %q", p, state)
	}

	for spec, accent := range map[string]string{
		"auto":               "#000002",
		"omarchy":            "#000002",
		"coral":              Default().Accent,
		"kanagawa":           Builtin["kanagawa"].Accent,
		legacy:               "#000001",
		filepath.Dir(legacy): "#000001",
	} {
		s, err := Resolve(spec)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", spec, err)
		}
		th, err := s.Load()
		if err != nil || th.Accent != accent {
			t.Errorf("Resolve(%q).Load() accent = %q, %v; want %q", spec, th.Accent, err, accent)
		}
	}

	if _, err := Resolve("kanagwa"); err == nil {
		t.Error("Resolve of a misspelled theme name succeeded")
	}
	if _, err := (&Source{path: filepath.Join(home, "missing.toml")}).Load(); err == nil {
		t.Error("loading a missing file succeeded")
	}
}

func TestChanged(t *testing.T) {
	dir := t.TempDir()
	p := writeColors(t, dir, `accent = "#000001"`)
	s, _ := Resolve(dir)
	if _, err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Changed(); ok {
		t.Error("Changed reported a change for an unchanged file")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Changed(); ok {
		t.Error("Changed reported a change for a missing file")
	}
	writeColors(t, dir, `accent = "#000003"`)
	if th, ok := s.Changed(); !ok || th.Accent != "#000003" {
		t.Errorf("Changed = %+v, %v after switching themes", th, ok)
	}
	if _, ok := s.Changed(); ok {
		t.Error("Changed reported the same change twice")
	}

	var none *Source
	if _, ok := none.Changed(); ok {
		t.Error("nil Source reported a change")
	}
	if _, ok := (&Source{}).Changed(); ok {
		t.Error("built-in Source reported a change")
	}
}

func TestBuiltinComplete(t *testing.T) {
	for name, th := range Builtin {
		for _, c := range []string{th.Accent, th.Fg, th.Muted, th.Border, th.SelBg, th.SelBgLo, th.BarBg, th.LogoFg,
			th.Green, th.Yellow, th.Red, th.Blue, th.Cyan, th.Purple, th.Orange} {
			if !isHex(c) {
				t.Errorf("%s: %q is not a hex color", name, c)
			}
		}
	}
}

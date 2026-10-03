package config

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestPinsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "pins.json")
	if pins, err := LoadPins(path); err != nil || pins != nil {
		t.Fatalf("missing file: got %v, %v", pins, err)
	}
	want := []Pin{{Context: "prod"}, {Context: "dev", Namespace: "shop"}}
	if err := SavePins(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPins(path)
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("got %v, %v; want %v", got, err, want)
	}
}

func TestFieldsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fields.json")
	f, err := LoadFields(path)
	if err != nil {
		t.Fatal(err)
	}
	f.SetFavorite("Pod", ".spec.nodeName", true)
	f.SetHidden("Pod", ".status", true)
	f.SetHidden("Pod", ".spec.nodeName", true) // hiding drops the favorite
	if f.IsFavorite("Pod", ".spec.nodeName") || !f.IsHidden("Pod", ".spec.nodeName") {
		t.Fatal("hide should replace favorite")
	}
	if err := SaveFields(path, f); err != nil {
		t.Fatal(err)
	}
	g, err := LoadFields(path)
	if err != nil || !g.IsHidden("Pod", ".status") || g.IsHidden("Deployment.apps", ".status") {
		t.Fatalf("got %+v, %v", g.Kinds, err)
	}
	g.SetHidden("Pod", ".status", false)
	g.SetHidden("Pod", ".spec.nodeName", false)
	if len(g.Kinds) != 0 {
		t.Fatalf("empty kinds should be pruned: %+v", g.Kinds)
	}
}

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

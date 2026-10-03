package config

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
	if demo {
		return Path("pins-demo.json")
	}
	return Path("pins.json")
}

// LoadPins reads the pins at path. A missing file is no pins.
func LoadPins(path string) ([]Pin, error) {
	var f pinsFile
	err := load(path, &f)
	return f.Pins, err
}

// SavePins writes pins to path. An empty path saves nothing.
func SavePins(path string, pins []Pin) error {
	return save(path, pinsFile{Pins: pins})
}

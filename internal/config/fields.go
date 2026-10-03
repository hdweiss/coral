package config

import "slices"

// Fields holds favorite and hidden fields of the YAML view per kind, keyed
// by group kind ("Pod", "Deployment.apps"). Paths are yamltree patterns such
// as .spec.containers[].image.
type Fields struct {
	Kinds map[string]*KindFields `json:"kinds"`
}

type KindFields struct {
	Favorites []string `json:"favorites,omitempty"`
	Hidden    []string `json:"hidden,omitempty"`
}

// FieldsPath returns where field preferences are stored.
func FieldsPath() string { return Path("fields.json") }

// LoadFields reads the field preferences at path. A missing file is empty.
func LoadFields(path string) (*Fields, error) {
	f := &Fields{}
	err := load(path, f)
	if f.Kinds == nil {
		f.Kinds = map[string]*KindFields{}
	}
	return f, err
}

// SaveFields writes f to path. An empty path saves nothing.
func SaveFields(path string, f *Fields) error { return save(path, f) }

func (f *Fields) IsFavorite(kind, path string) bool {
	k := f.Kinds[kind]
	return k != nil && slices.Contains(k.Favorites, path)
}

func (f *Fields) IsHidden(kind, path string) bool {
	k := f.Kinds[kind]
	return k != nil && slices.Contains(k.Hidden, path)
}

// SetFavorite marks or unmarks path as a favorite. A favorite is never hidden.
func (f *Fields) SetFavorite(kind, path string, on bool) {
	k := f.kind(kind)
	k.Favorites = set(k.Favorites, path, on)
	if on {
		k.Hidden = set(k.Hidden, path, false)
	}
	f.prune(kind)
}

// SetHidden hides or unhides path. A hidden field is never a favorite.
func (f *Fields) SetHidden(kind, path string, on bool) {
	k := f.kind(kind)
	k.Hidden = set(k.Hidden, path, on)
	if on {
		k.Favorites = set(k.Favorites, path, false)
	}
	f.prune(kind)
}

func (f *Fields) kind(kind string) *KindFields {
	if f.Kinds[kind] == nil {
		f.Kinds[kind] = &KindFields{}
	}
	return f.Kinds[kind]
}

func (f *Fields) prune(kind string) {
	if k := f.Kinds[kind]; len(k.Favorites) == 0 && len(k.Hidden) == 0 {
		delete(f.Kinds, kind)
	}
}

func set(list []string, s string, on bool) []string {
	i := slices.Index(list, s)
	switch {
	case on && i < 0:
		return append(list, s)
	case !on && i >= 0:
		return slices.Delete(list, i, i+1)
	}
	return list
}

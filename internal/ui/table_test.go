package ui

import (
	"slices"
	"testing"

	"github.com/hdweiss/coral/internal/k8s"
)

func TestLayoutColumns(t *testing.T) {
	cols := []k8s.Column{
		{Name: "NAME"}, {Name: "READY"}, {Name: "STATUS"},
		{Name: "IP", Drop: 2}, {Name: "NODE", Drop: 1}, {Name: "AGE"},
	}
	natural := []int{30, 5, 16, 11, 20, 4}
	// 1 + 86 + 5 gaps*2 = 97
	tests := []struct {
		avail int
		want  []int
	}{
		{97, []int{30, 5, 16, 11, 20, 4}}, // fits
		{91, []int{24, 5, 16, 11, 20, 4}}, // NAME shrinks to its soft minimum
		{90, []int{24, 5, 16, 0, 20, 4}},  // then IP goes
		{78, []int{24, 5, 16, 0, 20, 4}},  // still fits without NODE
		{77, []int{24, 5, 16, 0, 0, 4}},   // then NODE
		{50, []int{18, 5, 16, 0, 0, 4}},   // then NAME shrinks further
		{40, []int{12, 5, 16, 0, 0, 0}},   // then columns go from the right
		{20, []int{12, 5, 0, 0, 0, 0}},    // NAME never goes below its hard minimum
	}
	for _, tt := range tests {
		if got := layoutColumns(cols, natural, tt.avail, 0); !slices.Equal(got, tt.want) {
			t.Errorf("avail %d: got %v, want %v", tt.avail, got, tt.want)
		}
	}
}

func TestLayoutColumnsShortName(t *testing.T) {
	cols := []k8s.Column{{Name: "NAME"}, {Name: "IP", Drop: 1}}
	// A short NAME is never padded up to the soft minimum.
	if got := layoutColumns(cols, []int{8, 11}, 15, 0); !slices.Equal(got, []int{8, 0}) {
		t.Errorf("got %v", got)
	}
}

func TestLayoutColumnsKeepsSortColumn(t *testing.T) {
	cols := []k8s.Column{{Name: "NAME"}, {Name: "IP", Drop: 2}, {Name: "NODE", Drop: 1}}
	// Sorted by IP: NODE goes instead, even though IP would normally go first.
	if got := layoutColumns(cols, []int{10, 11, 10}, 30, 1); !slices.Equal(got, []int{10, 11, 0}) {
		t.Errorf("got %v", got)
	}
}

func TestLayoutColumnsFlexTakesLeftover(t *testing.T) {
	// Events: MESSAGE is the flex column; hiding OBJECT frees room for it.
	cols := []k8s.Column{{Name: "LAST SEEN"}, {Name: "TYPE", Drop: 2}, {Name: "OBJECT"}, {Name: "MESSAGE", Flex: true}}
	got := layoutColumns(cols, []int{10, 7, 40, 100}, 80, 0)
	if !slices.Equal(got, []int{10, 0, 40, 25}) {
		t.Errorf("got %v", got)
	}
	got = layoutColumns(cols, []int{10, 7, 40, 100}, 50, 0)
	if !slices.Equal(got, []int{10, 0, 0, 37}) {
		t.Errorf("narrow: got %v", got)
	}
}

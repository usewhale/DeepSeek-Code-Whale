package tui

import "testing"

func TestWrapSelectionCyclesWithinBounds(t *testing.T) {
	tests := []struct {
		name     string
		selected int
		count    int
		delta    int
		want     int
	}{
		{name: "down from first", selected: 0, count: 4, delta: 1, want: 1},
		{name: "up from middle", selected: 2, count: 4, delta: -1, want: 1},
		{name: "down from last wraps to first", selected: 3, count: 4, delta: 1, want: 0},
		{name: "up from first wraps to last", selected: 0, count: 4, delta: -1, want: 3},
		{name: "single item stays", selected: 0, count: 1, delta: -1, want: 0},
		{name: "single item down stays", selected: 0, count: 1, delta: 1, want: 0},
		{name: "multiple steps wrap", selected: 2, count: 3, delta: 2, want: 1},
		{name: "empty list yields zero", selected: 0, count: 0, delta: -1, want: 0},
		{name: "negative count yields zero", selected: 3, count: -2, delta: 1, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wrapSelection(tt.selected, tt.count, tt.delta); got != tt.want {
				t.Fatalf("wrapSelection(%d, %d, %d) = %d, want %d", tt.selected, tt.count, tt.delta, got, tt.want)
			}
		})
	}
}

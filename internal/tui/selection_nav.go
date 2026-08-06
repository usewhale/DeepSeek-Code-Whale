package tui

// wrapSelection cycles a list selection by delta within [0, count).
// Moving up (-1) from the first item lands on the last item, and moving
// down (+1) from the last item lands on the first item. Empty lists
// (count <= 0) always yield 0 so callers can wrap without extra guards.
func wrapSelection(selected, count, delta int) int {
	if count <= 0 {
		return 0
	}
	return ((selected+delta)%count + count) % count
}

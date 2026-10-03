package main

import "testing"

func TestThreshold(t *testing.T) {
	for _, c := range []struct{ v, want float64 }{{0, 0.05}, {1, 1.15}, {12.34, 13.63}} {
		if got := pinned(c.v); got != c.want {
			t.Errorf("pinned(%v) = %v, want %v", c.v, got, c.want)
		}
	}
}

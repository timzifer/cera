package cera

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// The tests draw small pages with several workers to hold bands
	// against one pass; workersFor would draw them in one pass.
	adaptWorkers = false
	os.Exit(m.Run())
}

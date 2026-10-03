package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

// threshold is pinned per corpus file: its worst page may differ from
// PDFium in at most Over percent of its pixels. Thresholds only go down,
// in the pull request that improves a file. Note says why a threshold is
// not PDFium's (ADR 0010: where PDFium is known to be wrong it is set from
// a second reference, MuPDF or pdf.js).
type threshold struct {
	Over float64 `json:"over"`
	Note string  `json:"note,omitempty"`
}

// thresholds maps corpus file names (as in internal/corpus/manifest.json,
// synthetic/… for the drawings) to their threshold.
type thresholds map[string]threshold

func readThresholds(path string) (thresholds, error) {
	th := thresholds{}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return th, nil
	}
	if err != nil {
		return nil, err
	}
	return th, json.Unmarshal(data, &th)
}

func (th thresholds) write(path string) error {
	data, err := json.MarshalIndent(th, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Package psfixtures locates the committed PS parity fixtures.
package psfixtures

import (
	"fmt"
	"os"
	"path/filepath"
)

// Dir walks up from the current working directory to find testdata/psfixtures.
func Dir() (string, error) {
	d, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(d, "testdata", "psfixtures")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("testdata/psfixtures not found above %s", d)
		}
		d = parent
	}
}

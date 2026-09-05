package main

import (
	"path/filepath"
	"runtime"
	"strings"
)

// validateSubPath checks if sub is a safe subdirectory of base.
// Returns true if filepath.Join(base, sub) stays under base.
func validateSubPath(base, sub string) bool {
	full := filepath.Join(base, sub, "dummy")
	return strings.HasPrefix(filepath.Clean(full), filepath.Clean(base))
}

// tempCheckDir returns a platform-specific temp directory for path validation.
func tempCheckDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join("C:", "Windows", "Temp", "boltgo-check")
	}
	return filepath.Join("/tmp", "boltgo-check")
}

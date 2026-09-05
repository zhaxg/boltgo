package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

const (
	maxLogSize  = 10 * 1024 * 1024 // 10MB
	maxLogFiles = 2                 // keep boltgo.log.1 and boltgo.log.2
)

// setupFileLogging enables logging to <dest>/boltgo.log with rotation.
func setupFileLogging(dest string) {
	rotateLog(dest)

	logPath := filepath.Join(dest, "boltgo.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot open log file %s: %v\n", logPath, err)
		return
	}

	// Tee: write to both stderr and file
	multiWriter := io.MultiWriter(os.Stderr, f)
	log.SetOutput(multiWriter)
}

// rotateLog rotates boltgo.log if it exceeds maxLogSize.
func rotateLog(dir string) {
	logPath := filepath.Join(dir, "boltgo.log")

	// Check if log exists and exceeds max size
	info, err := os.Stat(logPath)
	if err != nil || info.Size() < maxLogSize {
		return
	}

	// Delete oldest: boltgo.log.2
	oldest := fmt.Sprintf("%s.%d", logPath, maxLogFiles)
	os.Remove(oldest)

	// Shift: .1 → .2, .log → .1
	for i := maxLogFiles; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", logPath, i)
		dst := fmt.Sprintf("%s.%d", logPath, i+1)
		if i == maxLogFiles {
			os.Remove(dst) // remove oldest
		}
		os.Rename(src, dst)
	}

	// Current → .1
	os.Rename(logPath, fmt.Sprintf("%s.1", logPath))
}

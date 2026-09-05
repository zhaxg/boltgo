package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

const (
	maxLogSize    = 10 * 1024 * 1024
	maxLogFiles   = 2
	flushInterval = 2 * time.Second
)

var logChan chan []byte

// setupFileLogging enables async logging to <dest>/boltgo.log.
func setupFileLogging(dest string, isService bool) {
	os.MkdirAll(dest, 0755)
	rotateLog(dest)

	logPath := filepath.Join(dest, "boltgo.log")
	logChan = make(chan []byte, 1000)
	go asyncLogWriter(logPath)

	if isService {
		log.SetOutput(&channelWriter{})
	} else {
		log.SetOutput(io.MultiWriter(os.Stderr, &channelWriter{}))
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
}

func asyncLogWriter(path string) {
	var buf [][]byte
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(buf) == 0 {
			return
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return
		}
		for _, line := range buf {
			f.Write(line)
		}
		f.Close()
		buf = buf[:0]
	}

	for {
		select {
		case line := <-logChan:
			buf = append(buf, line)
			if len(buf) >= 100 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

type channelWriter struct{}

func (w *channelWriter) Write(p []byte) (int, error) {
	line := make([]byte, len(p))
	copy(line, p)
	select {
	case logChan <- line:
	default:
	}
	return len(p), nil
}

func rotateLog(dir string) {
	logPath := filepath.Join(dir, "boltgo.log")
	info, err := os.Stat(logPath)
	if err != nil || info.Size() < maxLogSize {
		return
	}
	oldest := logPath + ".2"
	os.Remove(oldest)
	for i := maxLogFiles; i >= 1; i-- {
		src := logPath + "." + string(rune('0'+i))
		dst := logPath + "." + string(rune('0'+i+1))
		if i == maxLogFiles {
			os.Remove(dst)
		}
		os.Rename(src, dst)
	}
	os.Rename(logPath, logPath+".1")
}

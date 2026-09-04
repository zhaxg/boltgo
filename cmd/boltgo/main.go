package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// version is set at build time via ldflags: -ldflags="-X main.version=v0.1.0"
var version = "dev"

// Exit codes (inspired by robocopy)
const (
	ExitSuccess         = 0  // All files transferred successfully
	ExitSuccessCopied   = 1  // Success: files were copied
	ExitSuccessSkipped  = 2  // Success: files were skipped (dedup)
	ExitSuccessMixed    = 3  // Success: some copied, some skipped
	ExitErrorConn       = 8  // Connection error (cannot reach server)
	ExitErrorTLS        = 9  // TLS/handshake error
	ExitErrorPartial    = 10 // Partial failure (some files failed)
	ExitErrorAll        = 11 // All files failed
	ExitFatal           = 16 // Fatal error (bad args, path not found, etc.)
)

// Global verbose flag
var verbose bool

type milliWriter struct{}

func (milliWriter) Write(p []byte) (int, error) {
	ts := time.Now().Format("2006/01/02 15:04:05.000")
	return os.Stderr.Write(append([]byte(ts+" "), p...))
}

// logInfo logs at info level (always shown)
func logInfo(format string, v ...interface{}) {
	log.Printf(format, v...)
}

// logVerbose logs at verbose level (only shown with -v)
func logVerbose(format string, v ...interface{}) {
	if verbose {
		log.Printf(format, v...)
	}
}

// ExitError represents an error with an exit code
type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string {
	return e.Message
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(ExitFatal)
	}

	args := os.Args[1:]
	filtered := args[:0]
	for _, a := range args {
		if a == "-v" || a == "--verbose" {
			verbose = true
		} else {
			filtered = append(filtered, a)
		}
	}
	args = filtered

	log.SetOutput(milliWriter{})
	log.SetFlags(0)

	switch args[0] {
	case "send":
		cmdSend(args[1:])
	case "receive":
		cmdReceive(args[1:])
	case "probe":
		cmdProbe(args[1:])
	case "version":
		fmt.Printf("boltgo %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", args[0])
		printUsage()
		os.Exit(ExitFatal)
	}
}

func printUsage() {
	fmt.Print(`boltgo — QUIC file transfer

Usage:
  boltgo send <file|dir> <host:port> [remote-path] [flags]
  boltgo receive [flags]
  boltgo probe <host:port>
  boltgo version

Global flags:
  -v, --verbose    Verbose log output

Send flags:
  --no-verify      Skip SHA-256 integrity check on receiver side
  --parallel       Max concurrent transfers (default: 10)
  --retry          Retry attempts per file (default: 3)
  --small-threshold Files below this size use fast path, no receipt (default: 256KB)

Receive flags:
  --port           QUIC listen port (default: 7879)
  --save-to        Directory to save received files (default: ./received)
  --bind           Bind address (default: 0.0.0.0)

Examples:
  boltgo -v receive --save-to ./inbox --port 7879
  boltgo send ./data.bin 127.0.0.1:7879
  boltgo send ./data.bin 127.0.0.1:7879 /subdir
  boltgo send --no-verify ./project 192.168.1.10:7879 /dest
`)
}

func cmdSend(args []string) {
	// manually parse flags in any order, then positional args
	noVerify := false
	parallel := 10
	retry := 3
	smallThreshold := int64(256 * 1024)
	var positional []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-verify":
			noVerify = true
		case "--parallel":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &parallel)
				i++
			}
		case "--retry":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &retry)
				i++
			}
		case "--small-threshold":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &smallThreshold)
				i++
			}
		default:
			positional = append(positional, args[i])
		}
	}

	if len(positional) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: boltgo send [flags] <file|dir> <host:port> [remote-path]\n")
		os.Exit(ExitFatal)
	}

	srcPath := positional[0]
	serverAddr := positional[1]
	remotePrefix := ""
	if len(positional) > 2 {
		remotePrefix = strings.TrimLeft(positional[2], "/\\")
		remotePrefix = strings.ReplaceAll(remotePrefix, "\\", "/")
	}

	info, err := os.Stat(srcPath)
	if os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "path not found: %s\n", srcPath)
		os.Exit(ExitFatal)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\ninterrupted")
		cancel()
	}()

	cfg := ClientConfig{
		ServerAddr:     serverAddr,
		DevMode:        true,
		NoVerify:       noVerify,
		MaxConcurrent:  parallel,
		RetryAttempts:  retry,
		SmallThreshold: uint64(smallThreshold),
	}

	if info.IsDir() {
		absPath, _ := filepath.Abs(srcPath)
		if err := SendDir(ctx, cfg, absPath, remotePrefix); err != nil {
			if exitErr, ok := err.(*ExitError); ok {
				fmt.Fprintf(os.Stderr, "send error: %s\n", exitErr.Message)
				os.Exit(exitErr.Code)
			}
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			os.Exit(ExitErrorAll)
		}
	} else {
		remoteName := filepath.Base(srcPath)
		if remotePrefix != "" {
			remoteName = remotePrefix + "/" + remoteName
		}
		if err := SendFile(ctx, cfg, srcPath, remoteName); err != nil {
			if exitErr, ok := err.(*ExitError); ok {
				fmt.Fprintf(os.Stderr, "send error: %s\n", exitErr.Message)
				os.Exit(exitErr.Code)
			}
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			os.Exit(ExitErrorAll)
		}
	}
}

func cmdProbe(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: boltgo probe <host:port>\n")
		os.Exit(ExitFatal)
	}

	serverAddr := args[0]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	saveDir, err := Probe(ctx, serverAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe error: %v\n", err)
		os.Exit(ExitErrorConn)
	}

	fmt.Println(saveDir)
}

func cmdReceive(args []string) {
	saveTo := "./received"
	port := 7879
	bind := "0.0.0.0"

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--save-to":
			if i+1 < len(args) {
				saveTo = args[i+1]
				i++
			}
		case "--port":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &port)
				i++
			}
		case "--bind":
			if i+1 < len(args) {
				bind = args[i+1]
				i++
			}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nstopping...")
		cancel()
	}()

	cfg := ServerConfig{
		BindAddr:    bind,
		Port:        port,
		ReceiveDir:  saveTo,
		MaxFileSize: 0,
	}

	if err := RunServer(ctx, cfg); err != nil {
		if exitErr, ok := err.(*ExitError); ok {
			fmt.Fprintf(os.Stderr, "server error: %s\n", exitErr.Message)
			os.Exit(exitErr.Code)
		}
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(ExitFatal)
	}
}

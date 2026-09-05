package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
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

// Global debug flag
var debug bool

type milliWriter struct{}

func (milliWriter) Write(p []byte) (int, error) {
	ts := time.Now().Format("2006/01/02 15:04:05.000")
	return os.Stderr.Write(append([]byte(ts+" "), p...))
}

// logInfo logs at info level (always shown)
func logInfo(format string, v ...interface{}) {
	log.Printf(format, v...)
}

// logDebug logs at debug level (only shown with --debug)
func logDebug(format string, v ...interface{}) {
	if debug {
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

	// boltgo -v alone = show version (like node -v, go version)
	if len(args) == 1 && (args[0] == "-v" || args[0] == "--version") {
		fmt.Printf("boltgo %s\n", version)
		return
	}

	filtered := args[:0]
	for _, a := range args {
		if a == "--debug" {
			debug = true
		} else {
			filtered = append(filtered, a)
		}
	}
	args = filtered

	if len(args) == 0 {
		printUsage()
		os.Exit(ExitFatal)
	}

	log.SetOutput(milliWriter{})
	log.SetFlags(0)

	// Show logo for main commands only, not for probe/version/help
	switch args[0] {
	case "send", "recv":
		ShowLogo()
	}

	switch args[0] {
	case "send":
		cmdSend(args[1:])
	case "recv":
		if runtime.GOOS == "windows" && runAsWindowsService() {
			return // Service handled, exit
		}
		cmdReceive(args[1:])
	case "service":
		cmdService(args[1:])
	case "probe":
		cmdProbe(args[1:])
	case "version":
		fmt.Printf("boltgo %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s (use --help for usage)\n", args[0])
		os.Exit(ExitFatal)
	}
}

func printUsage() {
	fmt.Print(`boltgo — QUIC file transfer

Usage:
  boltgo send <file|dir> <host:port> [remote-path] [flags]
  boltgo recv [flags]
  boltgo probe <host:port>
  boltgo service <install|uninstall> [flags]
  boltgo version / boltgo -v

Global flags:
  --debug          Debug log output (use with subcommand)

Send flags:
  --parallel       Max concurrent transfers (default: 10)
  --retry          Retry attempts per file (default: 3)

Recv flags:
  --bind           Bind address (default: 0.0.0.0)
  --port           QUIC listen port (default: 7879)
  --dest           Directory to save received files (default: /tmp)

Service flags:
  --dest           Directory to save received files (default: /tmp)
  --port           QUIC listen port (default: 7879)
  --bind           Bind address (default: 0.0.0.0)

Examples:
  boltgo recv --dest ./inbox --port 7879
  boltgo send ./report.csv 192.168.1.10:7879
  boltgo send ./project 192.168.1.10:7879
  boltgo send ./data 192.168.1.10:7879 /subpath
  boltgo probe 192.168.1.10:7879
  boltgo service install --dest /tmp --port 7879

`)
}

func cmdSend(args []string) {
	// manually parse flags in any order, then positional args
	parallel := 10
	retry := 3
	var positional []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--parallel":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				fmt.Fprintln(os.Stderr, "Error: --parallel requires a value")
				os.Exit(ExitFatal)
			}
			fmt.Sscanf(args[i+1], "%d", &parallel)
			i++
		case "--retry":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				fmt.Fprintln(os.Stderr, "Error: --retry requires a value")
				os.Exit(ExitFatal)
			}
			fmt.Sscanf(args[i+1], "%d", &retry)
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "Error: unknown flag: %s (use --help for usage)\n", args[i])
				os.Exit(ExitFatal)
			}
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
		// Security: simulate server-side check
		if !validateSubPath(tempCheckDir(), remotePrefix) {
			fmt.Fprintln(os.Stderr, "Error: path traversal detected in remote path")
			os.Exit(ExitFatal)
		}
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
		MaxConcurrent:  parallel,
		RetryAttempts:  retry,
		SmallThreshold: uint64(256 * 1024),
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

// RecvConfig holds recv/service flags
type RecvConfig struct {
	Dest string
	Port int
	Bind string
}

// defaultRecvConfig returns platform-appropriate defaults
func defaultRecvConfig() RecvConfig {
	dest := "/tmp"
	if runtime.GOOS == "windows" {
		dest = "c:\\tmp"
	}
	return RecvConfig{Dest: dest, Port: 7879, Bind: "0.0.0.0"}
}

// parseRecvFlags parses --dest, --port, --bind with validation
func parseRecvFlags(args []string) RecvConfig {
	cfg := defaultRecvConfig()
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dest":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				fmt.Fprintln(os.Stderr, "Error: --dest requires a value")
				os.Exit(ExitFatal)
			}
			cfg.Dest = args[i+1]
			i++
		case "--port":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				fmt.Fprintln(os.Stderr, "Error: --port requires a value")
				os.Exit(ExitFatal)
			}
			if _, err := fmt.Sscanf(args[i+1], "%d", &cfg.Port); err != nil || cfg.Port < 1 || cfg.Port > 65535 {
				fmt.Fprintf(os.Stderr, "Error: --port must be a number (1-65535), got: %s\n", args[i+1])
				os.Exit(ExitFatal)
			}
			i++
		case "--bind":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				fmt.Fprintln(os.Stderr, "Error: --bind requires a value")
				os.Exit(ExitFatal)
			}
			cfg.Bind = args[i+1]
			i++
		default:
			fmt.Fprintf(os.Stderr, "Error: unknown flag: %s (use --help for usage)\n", args[i])
			os.Exit(ExitFatal)
		}
	}
	return cfg
}

func cmdReceive(args []string) {
	cfg := parseRecvFlags(args)

	// Setup file logging to <dest>/boltgo.log
	setupFileLogging(cfg.Dest, false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nstopping...")
		cancel()
	}()

	serverCfg := ServerConfig{
		BindAddr:    cfg.Bind,
		Port:        cfg.Port,
		ReceiveDir:  cfg.Dest,
		MaxFileSize: 0,
	}

	if err := RunServer(ctx, serverCfg); err != nil {
		if exitErr, ok := err.(*ExitError); ok {
			fmt.Fprintf(os.Stderr, "server error: %s\n", exitErr.Message)
			os.Exit(exitErr.Code)
		}
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(ExitFatal)
	}
}

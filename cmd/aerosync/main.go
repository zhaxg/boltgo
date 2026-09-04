package main

import (
	"boltgo/internal/quic"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

const version = "0.1.0-go"

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "send":
		cmdSend(os.Args[2:])
	case "receive":
		cmdReceive(os.Args[2:])
	case "version":
		fmt.Printf("aerosync-go %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`aerosync-go — QUIC file transfer

Usage:
  aerosync-go send   <file> <host:port>    Send a file to a receiver
  aerosync-go receive                       Start a file receiver
  aerosync-go version                       Print version

Receive flags:
  --dir       Directory to save received files (default: ./received)
  --port      QUIC listen port (default: 7789)
  --bind      Bind address (default: 0.0.0.0)
  --cert      TLS certificate file (PEM, optional)
  --key       TLS key file (PEM, optional)

Examples:
  aerosync-go receive --dir ./inbox --port 7789
  aerosync-go send ./data.bin 127.0.0.1:7789
  aerosync-go send ./photo.jpg 192.168.1.10
`)
}

func cmdSend(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: aerosync-go send <file> <host:port>\n")
		os.Exit(1)
	}

	filePath := args[0]
	serverAddr := args[1]

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "file not found: %s\n", filePath)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl-C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\ninterrupted")
		cancel()
	}()

	cfg := quic.ClientConfig{
		ServerAddr: serverAddr,
		DevMode:    true,
	}

	if err := quic.SendFile(ctx, cfg, filePath); err != nil {
		fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		os.Exit(1)
	}
}

func cmdReceive(args []string) {
	fs := flag.NewFlagSet("receive", flag.ExitOnError)
	dir := fs.String("dir", "./received", "directory to save received files")
	port := fs.Int("port", 7789, "QUIC listen port")
	bind := fs.String("bind", "0.0.0.0", "bind address")
	certFile := fs.String("cert", "", "TLS certificate file (PEM)")
	keyFile := fs.String("key", "", "TLS key file (PEM)")
	fs.Parse(args)

	if *certFile != "" && *keyFile == "" || *keyFile != "" && *certFile == "" {
		fmt.Fprintf(os.Stderr, "both --cert and --key must be specified\n")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl-C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nstopping...")
		cancel()
	}()

	cfg := quic.ServerConfig{
		BindAddr:    *bind,
		Port:        *port,
		ReceiveDir:  *dir,
		TLSCertFile: *certFile,
		TLSKeyFile:  *keyFile,
	}

	if err := quic.RunServer(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

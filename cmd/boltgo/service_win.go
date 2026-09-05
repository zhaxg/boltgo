//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows/svc"
)

type boltgoService struct{}

func (s *boltgoService) Execute(args []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	// Start the actual work in a goroutine
	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		errCh <- runRecvFromService(ctx)
	}()

	status <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case err := <-errCh:
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "service error: %v\n", err)
			}
			status <- svc.Status{State: svc.StopPending}
			return false, 1
		case <-ticker.C:
			// Report running periodically
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				cancel()
				status <- svc.Status{State: svc.StopPending}
				return false, 0
			}
		}
	}
}

func runRecvFromService(ctx context.Context) error {
	cfg := defaultRecvConfig()
	// Parse from service binPath args
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dest":
			if i+1 < len(args) {
				cfg.Dest = args[i+1]
				i++
			}
		case "--port":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &cfg.Port)
				i++
			}
		case "--bind":
			if i+1 < len(args) {
				cfg.Bind = args[i+1]
				i++
			}
		}
	}

	// Setup file logging for service mode
	setupFileLogging(cfg.Dest)

	serverCfg := ServerConfig{
		BindAddr:    cfg.Bind,
		Port:        cfg.Port,
		ReceiveDir:  cfg.Dest,
		MaxFileSize: 0,
	}

	return RunServer(ctx, serverCfg)
}

// runAsWindowsService tries to run as a Windows service.
// Returns true if running as a service (caller should exit).
func runAsWindowsService() bool {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return false
	}
	if !isService {
		return false
	}

	err = svc.Run("boltgo", &boltgoService{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "service failed: %v\n", err)
		os.Exit(ExitFatal)
	}
	return true
}

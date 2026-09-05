package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func cmdService(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: boltgo service <install|uninstall> [flags]")
		os.Exit(ExitFatal)
	}
	switch args[0] {
	case "install":
		cmdServiceInstall(args[1:])
	case "uninstall":
		cmdServiceUninstall()
	default:
		fmt.Fprintf(os.Stderr, "unknown service command: %s (use install or uninstall)\n", args[0])
		os.Exit(ExitFatal)
	}
}

func cmdServiceInstall(args []string) {
	cfg := parseRecvFlags(args)
	exePath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot determine executable path: %v\n", err)
		os.Exit(ExitFatal)
	}
	exePath, _ = filepath.Abs(exePath)

	if runtime.GOOS == "linux" {
		installSystemd(exePath, cfg)
	} else if runtime.GOOS == "windows" {
		installWindows(exePath, cfg)
	} else {
		fmt.Fprintf(os.Stderr, "Error: service install not supported on %s\n", runtime.GOOS)
		os.Exit(ExitFatal)
	}
}

func cmdServiceUninstall() {
	if runtime.GOOS == "linux" {
		uninstallSystemd()
	} else if runtime.GOOS == "windows" {
		uninstallWindows()
	} else {
		fmt.Fprintf(os.Stderr, "Error: service uninstall not supported on %s\n", runtime.GOOS)
		os.Exit(ExitFatal)
	}
}

func installSystemd(exePath string, cfg RecvConfig) {
	target := "/usr/local/bin/boltgo"
	alreadyInTarget := exePath == target

	if !alreadyInTarget {
		// Stop existing service first
		fmt.Println("Stopping existing service...")
		exec.Command("systemctl", "stop", "boltgo").Run()

		// Copy to /usr/local/bin
		fmt.Printf("Copying: %s -> %s\n", exePath, target)
		if err := copyFile(exePath, target); err != nil {
			fmt.Fprintf(os.Stderr, "Error: copy failed: %v\n", err)
			os.Exit(ExitFatal)
		}
	}

	// If service already exists, remove it first
	active, _ := exec.Command("systemctl", "is-active", "boltgo").CombinedOutput()
	enabled, _ := exec.Command("systemctl", "is-enabled", "boltgo").CombinedOutput()
	if strings.Contains(string(active), "active") || strings.Contains(string(enabled), "enabled") {
		fmt.Println("Service already exists, removing...")
		exec.Command("systemctl", "stop", "boltgo").Run()
		exec.Command("systemctl", "disable", "boltgo").Run()
		os.Remove("/etc/systemd/system/boltgo.service")
		exec.Command("systemctl", "daemon-reload").Run()
	}

	// Create unit file
	unit := fmt.Sprintf(`[Unit]
Description=boltgo receive service
After=network.target

[Service]
Type=simple
ExecStart=%s recv --dest %s --port %d --bind %s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, target, cfg.Dest, cfg.Port, cfg.Bind)

	unitPath := "/etc/systemd/system/boltgo.service"
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot write unit file: %v\n", err)
		os.Exit(ExitFatal)
	}

	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: daemon-reload failed: %v\n%s", err, out)
		os.Exit(ExitFatal)
	}
	if out, err := exec.Command("systemctl", "enable", "boltgo").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: enable failed: %v\n%s", err, out)
		os.Exit(ExitFatal)
	}

	fmt.Printf("Service installed: %s\n", unitPath)
	fmt.Printf("  boltgo service install --dest %s --port %d --bind %s\n", cfg.Dest, cfg.Port, cfg.Bind)
	fmt.Println()
	fmt.Println("Manage: systemctl start|stop|status boltgo")
	fmt.Println("Logs:   journalctl -u boltgo -f")
}

func uninstallSystemd() {
	unitPath := "/etc/systemd/system/boltgo.service"
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		fmt.Println("Service not installed, nothing to do.")
		return
	}

	exec.Command("systemctl", "stop", "boltgo").Run()
	exec.Command("systemctl", "disable", "boltgo").Run()

	if err := os.Remove(unitPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot remove unit file: %v\n", err)
		os.Exit(ExitFatal)
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: daemon-reload failed: %v\n%s", err, out)
		os.Exit(ExitFatal)
	}

	// Remove binary
	os.Remove("/usr/local/bin/boltgo")

	fmt.Println("Service uninstalled.")
}

func installWindows(exePath string, cfg RecvConfig) {
	// Check admin privileges
	if _, err := exec.Command("net", "session").CombinedOutput(); err != nil {
		fmt.Fprintln(os.Stderr, "Error: please run as Administrator")
		os.Exit(ExitFatal)
	}

	target := "C:\\Windows\\boltgo.exe"
	alreadyInTarget := strings.EqualFold(exePath, target)

	if !alreadyInTarget {
		// Stop existing service first (releases file lock)
		fmt.Println("Stopping existing service...")
		exec.Command("sc", "stop", "boltgo").Run()

		// Copy to C:\Windows
		fmt.Printf("Copying: %s -> %s\n", exePath, target)
		if err := copyFile(exePath, target); err != nil {
			fmt.Fprintf(os.Stderr, "Error: copy failed: %v\n", err)
			os.Exit(ExitFatal)
		}
	}

	// If service already exists, tell user to uninstall first
	_, queryErr := exec.Command("sc", "query", "boltgo").CombinedOutput()
	if queryErr == nil {
		fmt.Fprintln(os.Stderr, "Error: service already exists.")
		fmt.Fprintln(os.Stderr, "  Try: boltgo service uninstall")
		fmt.Fprintln(os.Stderr, "  Or:  sc delete boltgo")
		fmt.Fprintln(os.Stderr, "  If still fails, restart Windows and try again.")
		os.Exit(ExitFatal)
	}

	// Register service (use exec.Command directly for sc - cmd /c mangles sc's argument parsing)
	binLine := fmt.Sprintf("\"%s\" recv --dest \"%s\" --port %d --bind %s", target, cfg.Dest, cfg.Port, cfg.Bind)
	fmt.Println("Registering service...")

	scOut, scErr := exec.Command("sc", "create", "boltgo",
		"binPath=", binLine,
		"start=", "auto",
		"DisplayName=", "boltgo receive service",
	).CombinedOutput()
	scOutput := strings.TrimSpace(string(scOut))
	if scErr != nil {
		if strings.Contains(scOutput, "1072") {
			fmt.Fprintln(os.Stderr, "Error: service is marked for deletion.")
			fmt.Fprintln(os.Stderr, "  Please restart Windows, then run: boltgo service install --dest D:\\tmp --port 7879")
		} else {
			fmt.Fprintf(os.Stderr, "Error: sc create failed: %v\n%s\n", scErr, scOutput)
		}
		os.Exit(ExitFatal)
	}
	if scOutput != "" {
		fmt.Printf("  %s\n", scOutput)
	}

	fmt.Println("Service installed: boltgo")
	fmt.Printf("  boltgo service install --dest %s --port %d --bind %s\n", cfg.Dest, cfg.Port, cfg.Bind)
	fmt.Println()
	fmt.Println("Manage: sc start|stop|status boltgo")
}

func uninstallWindows() {
	_, queryErr := exec.Command("sc", "query", "boltgo").CombinedOutput()
	if queryErr != nil {
		fmt.Println("Service not installed, nothing to do.")
		return
	}

	fmt.Println("Stopping service...")
	exec.Command("sc", "stop", "boltgo").Run()

	fmt.Println("Deleting service...")
	deleteOut, deleteErr := exec.Command("sc", "delete", "boltgo").CombinedOutput()
	deleteStr := strings.TrimSpace(string(deleteOut))
	if deleteErr != nil {
		// 1060 = not installed, 1072 = already marked for deletion
		if strings.Contains(deleteStr, "1060") || strings.Contains(deleteStr, "1072") {
			fmt.Println("  Service already removed.")
		} else {
			fmt.Fprintf(os.Stderr, "Error: sc delete failed: %v\n%s\n", deleteErr, deleteStr)
			os.Exit(ExitFatal)
		}
	} else if deleteStr != "" {
		fmt.Printf("  %s\n", deleteStr)
	}

	fmt.Println("Service uninstalled.")
}

// copyFile copies a file from src to dst, preserving permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

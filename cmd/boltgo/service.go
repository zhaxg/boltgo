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
		psRun("Stop-Service boltgo -Force -ErrorAction SilentlyContinue")

		// Copy to C:\Windows
		fmt.Printf("Copying: %s -> %s\n", exePath, target)
		if err := copyFile(exePath, target); err != nil {
			fmt.Fprintf(os.Stderr, "Error: copy failed: %v\n", err)
			os.Exit(ExitFatal)
		}
	}

	// If service already exists, tell user to uninstall first
	if psRun("Get-Service boltgo -ErrorAction SilentlyContinue") != "" {
		fmt.Fprintln(os.Stderr, "Error: service already exists.")
		fmt.Fprintln(os.Stderr, "  Try: boltgo service uninstall")
		fmt.Fprintln(os.Stderr, "  If still fails, restart Windows and try again.")
		os.Exit(ExitFatal)
	}

	// Register service using PowerShell New-Service
	fmt.Println("Registering service...")
	psCmd := fmt.Sprintf(
		"New-Service -Name boltgo -BinaryPathName 'boltgo.exe recv --dest \"%s\" --port %d --bind %s' -StartupType Automatic -DisplayName 'boltgo receive service'",
		cfg.Dest, cfg.Port, cfg.Bind,
	)
	out := psRun(psCmd)
	if out != "" {
		fmt.Printf("  %s\n", out)
	}

	fmt.Println("Service installed: boltgo")
	fmt.Printf("  boltgo service install --dest %s --port %d --bind %s\n", cfg.Dest, cfg.Port, cfg.Bind)
	fmt.Println()
	fmt.Println("Manage: Start-Service boltgo / Stop-Service boltgo / Get-Service boltgo")
}

func uninstallWindows() {
	if psRun("Get-Service boltgo -ErrorAction SilentlyContinue") == "" {
		fmt.Println("Service not installed, nothing to do.")
		return
	}

	fmt.Println("Stopping service...")
	// Use sc.exe for stop (PS5 compatible, ignore GBK output)
	exec.Command("sc.exe", "stop", "boltgo").Run()

	fmt.Println("Deleting service...")
	// Use sc.exe delete (PS5 compatible, GBK output ignored)
	exec.Command("sc.exe", "delete", "boltgo").Run()

	fmt.Println("Service uninstalled.")
}

// psRun runs a PowerShell command and returns trimmed output.
func psRun(cmd string) string {
	out, err := exec.Command("powershell", "-NoProfile", "-Command", cmd).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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

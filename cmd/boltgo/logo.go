package main

import (
	"os"
	"strings"
)

const logo = `
 █████              ████   █████
░░███              ░░███  ░░███
 ░███████   ██████  ░███  ███████    ███████  ██████
 ░███░░███ ███░░███ ░███ ░░░███░    ███░░███ ███░░███
 ░███ ░███░███ ░███ ░███   ░███    ░███ ░███░███ ░███
 ░███ ░███░███ ░███ ░███   ░███ ███░███ ░███░███ ░███
 ████████ ░░██████  █████  ░░█████ ░░███████░░██████
░░░░░░░░   ░░░░░░  ░░░░░    ░░░░░   ░░░░░███ ░░░░░░
                                    ███ ░███
                                   ░░██████
                                    ░░░░░░
`

// ShowLogo prints the ASCII logo if not suppressed.
// Set BOLTGO_NO_LOGO=1 to hide.
func ShowLogo() {
	defer func() { recover() }() // Safety net for service mode
	if os.Getenv("BOLTGO_NO_LOGO") == "1" {
		return
	}
	if os.Stderr == nil {
		return
	}
	out := strings.TrimPrefix(logo, "\n")
	os.Stderr.WriteString(out)
}

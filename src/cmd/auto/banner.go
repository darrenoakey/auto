package main

import (
	"fmt"
	"os"
)

// aiAgentEnvVars are set by AI coding agents; the banner is auto-suppressed when
// any is present so tool output stays clean. This is a display choice only.
var aiAgentEnvVars = []string{"CLAUDECODE", "CODEX_SANDBOX", "GEMINI_CLI"}

// ANSI colour codes used by the banner.
const (
	clrCyan    = "\033[96m"
	clrYellow  = "\033[93m"
	clrGreen   = "\033[92m"
	clrMagenta = "\033[95m"
	clrBlue    = "\033[94m"
	clrRed     = "\033[91m"
	clrReset   = "\033[0m"
	clrBold    = "\033[1m"
)

// bannerText is the rendered vibrant auto banner, colours baked in.
var bannerText = "\n" +
	clrCyan + "╔═══════════════════════════════════════════════════════════════╗\n" +
	clrCyan + "║  " + clrYellow + clrBold + " ▄▄▄       █    ██ ▄▄▄█████▓ ▒█████  " + clrCyan + "                        ║\n" +
	clrCyan + "║  " + clrYellow + clrBold + "▒████▄     ██  ▓██▒▓  ██▒ ▓▒▒██▒  ██▒" + clrCyan + "                        ║\n" +
	clrCyan + "║  " + clrGreen + clrBold + "▒██  ▀█▄  ▓██  ▒██░▒ ▓██░ ▒░▒██░  ██▒" + clrCyan + "                        ║\n" +
	clrCyan + "║  " + clrGreen + clrBold + "░██▄▄▄▄██ ▓▓█  ░██░░ ▓██▓ ░ ▒██   ██░" + clrCyan + "                        ║\n" +
	clrCyan + "║  " + clrMagenta + clrBold + " ▓█   ▓██▒▒▒█████▓   ▒██▒ ░ ░ ████▓▒░" + clrCyan + "                        ║\n" +
	clrCyan + "║  " + clrMagenta + clrBold + " ▒▒   ▓▒█░░▒▓▒ ▒ ▒   ▒ ░░   ░ ▒░▒░▒░ " + clrCyan + "                        ║\n" +
	clrCyan + "║  " + clrBlue + clrBold + "  ▒   ▒▒ ░░░▒░ ░ ░     ░      ░ ▒ ▒░ " + clrCyan + "                        ║\n" +
	clrCyan + "║  " + clrBlue + clrBold + "  ░   ▒    ░░░ ░ ░   ░      ░ ░ ░ ▒  " + clrCyan + "                        ║\n" +
	clrCyan + "║  " + clrRed + clrBold + "      ░  ░   ░                  ░ ░  " + clrCyan + "                        ║\n" +
	clrCyan + "║                                                               ║\n" +
	clrCyan + "║      " + clrGreen + "Daemon Process Manager" + clrCyan + " - " + clrMagenta + "Keep Your Services Running" + clrCyan + "      ║\n" +
	clrCyan + "╚═══════════════════════════════════════════════════════════════╝" + clrReset + "\n"

// maybeShowBanner prints the banner unless suppressed by -q or an AI agent
// environment, or forced on by --banner.
func maybeShowBanner(quiet, force bool) {
	if force || (!quiet && !runningUnderAgent()) {
		fmt.Print(bannerText)
	}
}

// runningUnderAgent reports whether an AI agent environment variable is set.
func runningUnderAgent() bool {
	for _, v := range aiAgentEnvVars {
		if os.Getenv(v) != "" {
			return true
		}
	}
	return false
}

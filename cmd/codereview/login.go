package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/nicolaeser/codereview/internal/auth"
	"github.com/nicolaeser/codereview/internal/logx"
)

func runLogin(parent context.Context, args []string) int {
	fs := flag.NewFlagSet("codereview login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	providerFlag := fs.String("provider", "", "grok/xai or codex")
	if argsWantHelp(args) {
		// Parse would treat `login grok -h` as a provider and start device-code.
		fs.Usage = loginUsage
		loginUsage()
		return exitOK
	}
	fs.Usage = loginUsage
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return exitOK
		}
		return exitUsage
	}
	provider := strings.TrimSpace(*providerFlag)
	if provider == "" && fs.NArg() > 0 {
		provider = fs.Arg(0)
	}
	if argsWantHelp(fs.Args()) {
		loginUsage()
		return exitOK
	}
	return finishLogin(parent, provider)
}

func loginUsage() {
	fmt.Fprintf(os.Stderr, `CodeReview login — store a local provider credential

Usage:
  codereview login grok
  codereview login xai
  codereview login codex

grok (and the xai alias) starts the SpaceXAI device-code flow for a Grok
subscription (SuperGrok / X Premium+). Same API host as AI_PROVIDER=xai.
Metered API-key users should set AI_PROVIDER=xai and AI_API_KEY instead.

codex starts the public ChatGPT/Codex JSON device-code flow and stores tokens
locally. No local HTTP server is used.

CI jobs should keep using AI_API_KEY.

Flags:
  -h, --help      show this help
  -provider name  grok/xai or codex
`)
}

func argsWantHelp(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" || arg == "-help" {
			return true
		}
	}
	return false
}

func finishLogin(parent context.Context, provider string) int {
	switch strings.ToLower(provider) {
	case "xai", "grok", "grok-build":
		if err := auth.LoginXAI(parent, auth.DeviceOptions{Stdout: os.Stdout}); err != nil {
			logx.New(os.Stderr, "error").Error("Grok login failed", "error", err)
			return exitFailure
		}
		fmt.Fprintf(os.Stdout, "Logged in to Grok subscription. Set AI_PROVIDER=grok (same API as xai).\n")
		return exitOK
	case "openai", "codex", "chatgpt":
		if err := auth.LoginCodex(parent, auth.DeviceOptions{Stdout: os.Stdout}); err != nil {
			logx.New(os.Stderr, "error").Error("Codex login failed", "error", err)
			return exitFailure
		}
		fmt.Fprintf(os.Stdout, "Logged in to Codex subscription. Set AI_PROVIDER=codex.\n")
		return exitOK
	case "":
		fmt.Fprintln(os.Stderr, "provider is required: grok or codex")
		loginUsage()
		return exitUsage
	default:
		fmt.Fprintf(os.Stderr, "unsupported login provider %q (use grok or codex)\n", provider)
		return exitUsage
	}
}

func runLogout(_ context.Context, args []string) int {
	if argsWantHelp(args) {
		fmt.Fprintln(os.Stderr, "Usage: codereview logout [grok|codex]")
		return exitOK
	}
	fs := flag.NewFlagSet("codereview logout", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	providerFlag := fs.String("provider", "", "grok or codex (default: both)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return exitOK
		}
		return exitUsage
	}
	provider := strings.TrimSpace(*providerFlag)
	if provider == "" && fs.NArg() > 0 {
		provider = fs.Arg(0)
	}
	providers := []string{"grok", "codex"}
	if provider != "" {
		switch strings.ToLower(provider) {
		case "xai", "grok", "grok-build":
			providers = []string{"grok"}
		case "openai", "codex", "chatgpt":
			providers = []string{"codex"}
		default:
			fmt.Fprintf(os.Stderr, "unsupported logout provider %q\n", provider)
			return exitUsage
		}
	}
	for _, name := range providers {
		if err := auth.DeleteToken(name); err != nil {
			logx.New(os.Stderr, "error").Error("logout failed", "provider", name, "error", err)
			return exitFailure
		}
	}
	fmt.Fprintln(os.Stdout, "Removed stored CodeReview credentials.")
	return exitOK
}

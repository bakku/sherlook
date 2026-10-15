// Command sherlook is a terminal client for Microsoft 365 shared mailboxes.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"bakku.dev/sherlook/internal/config"
	"bakku.dev/sherlook/internal/graph"
	"bakku.dev/sherlook/internal/ui"
)

const debugLogFile = "sherlook-debug.log"

func main() {
	debug := flag.Bool("debug", false, "write a request log (no tokens, secrets or mail content) to ./"+debugLogFile)
	configPath := flag.String("config", "", "config file (default: <user config dir>/sherlook/config.toml)")
	flag.Parse()

	if err := run(*configPath, *debug); err != nil {
		fmt.Fprintln(os.Stderr, "sherlook:", err)
		os.Exit(1)
	}
}

func run(configPath string, debug bool) error {
	if configPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return fmt.Errorf("locating config directory: %w", err)
		}
		configPath = p
	}

	httpClient := &http.Client{Timeout: 60 * time.Second}
	if debug {
		f, err := os.OpenFile(debugLogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		logger := log.New(f, "", log.LstdFlags|log.Lmicroseconds)
		logger.Printf("sherlook starting; config %s", configPath)
		httpClient.Transport = &graph.LoggingTransport{Base: http.DefaultTransport, Logger: logger}
	}

	store, warnings, err := config.Open(configPath, config.OSKeyring{})
	if err != nil {
		return err
	}

	newClient := func(mb config.Mailbox, secret string) ui.MailClient {
		return graph.New(mb.Address, graph.Credentials{
			TenantID:     mb.TenantID,
			ClientID:     mb.ClientID,
			ClientSecret: secret,
		}, graph.Options{HTTPClient: httpClient})
	}

	_, err = tea.NewProgram(ui.New(store, newClient, warnings), tea.WithAltScreen()).Run()
	return err
}

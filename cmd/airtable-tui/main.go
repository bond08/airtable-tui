// Command airtable-tui is a terminal client for Airtable.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bond08/airtable-tui/internal/airtable"
	"github.com/bond08/airtable-tui/internal/config"
	"github.com/bond08/airtable-tui/internal/setup"
	"github.com/bond08/airtable-tui/internal/termtheme"
	"github.com/bond08/airtable-tui/internal/ui"
)

// version is overridden at release build time via:
//
//	go build -ldflags "-X main.version=v1.2.3"
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	showVersion := flag.Bool("version", false, "print the version and exit")
	reconfigure := flag.Bool("reconfigure", false, "run setup again (choose a new token/base/table)")
	accent := flag.String("accent", "", "set the UI accent color (hex like #FF6AC1, or an ANSI index 0-15) and exit; pass \"auto\" to clear the override and go back to the terminal-derived default")
	flag.Parse()

	if *showVersion {
		fmt.Println("airtable-tui " + version)
		return nil
	}

	if *accent != "" {
		return setAccent(*accent)
	}

	cfg, err := config.Load()
	if err != nil || *reconfigure {
		cfg, err = setup.Run()
		if err != nil {
			return err
		}
	}

	// Priority: config/flag override > detected terminal theme > ui default.
	accentColor := cfg.Accent
	if accentColor == "" {
		accentColor = termtheme.DetectAccent()
	}

	client := airtable.New(cfg.PAT, cfg.BaseID)
	model := ui.New(client, cfg.Table, accentColor)

	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// setAccent updates just the accent color in the existing config, without
// re-running the full setup flow.
func setAccent(value string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("run setup first (no config found): %w", err)
	}
	if value == "auto" {
		cfg.Accent = ""
	} else {
		cfg.Accent = value
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	if cfg.Accent == "" {
		fmt.Println("Accent override cleared -- back to the terminal-derived default.")
	} else {
		fmt.Printf("Accent set to %s.\n", cfg.Accent)
	}
	return nil
}

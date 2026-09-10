// Package setup implements the first-run interactive onboarding flow: it
// asks for a Personal Access Token, verifies it, lets the user pick a base
// and table, and saves the result to the config file. It runs as a plain
// terminal prompt sequence (not Bubbletea) since it happens before the TUI
// program starts and needs simple line-based input.
package setup

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/bond08/airtable-tui/internal/airtable"
	"github.com/bond08/airtable-tui/internal/config"
)

// Run walks the user through first-time setup and returns the resulting
// config, already saved to disk.
func Run() (config.Config, error) {
	fmt.Println("Airtable TUI setup")
	fmt.Println("------------------")
	fmt.Println()
	fmt.Println("You'll need a Personal Access Token. Create one at:")
	fmt.Println("  https://airtable.com/create/tokens")
	fmt.Println("Required scopes: data.records:read, data.records:write, schema.bases:read")
	fmt.Println("Grant it access to whichever base(s) you want to use.")
	fmt.Println()

	pat, err := readSecret("Personal Access Token: ")
	if err != nil {
		return config.Config{}, err
	}
	pat = strings.TrimSpace(pat)
	if pat == "" {
		return config.Config{}, fmt.Errorf("no token entered")
	}

	client := airtable.New(pat, "")
	ctx := context.Background()

	fmt.Println("Verifying token and fetching your bases...")
	bases, err := client.ListBases(ctx)
	if err != nil {
		return config.Config{}, fmt.Errorf("checking token: %w", err)
	}
	if len(bases) == 0 {
		return config.Config{}, fmt.Errorf("that token can't see any bases -- check its access permissions in Airtable")
	}
	sort.Slice(bases, func(i, j int) bool { return bases[i].Name < bases[j].Name })

	base, err := chooseOne("base", bases, func(b airtable.Base) string { return b.Name })
	if err != nil {
		return config.Config{}, err
	}

	baseClient := airtable.New(pat, base.ID)
	tables, err := baseClient.ListTables(ctx)
	if err != nil {
		return config.Config{}, fmt.Errorf("fetching tables: %w", err)
	}
	if len(tables) == 0 {
		return config.Config{}, fmt.Errorf("base %q has no tables", base.Name)
	}

	table, err := chooseOne("table", tables, func(t airtable.Table) string { return t.Name })
	if err != nil {
		return config.Config{}, err
	}

	fmt.Println()
	fmt.Println("The UI's accent color normally matches your terminal's own theme.")
	fmt.Println("If that doesn't look right (e.g. a light/dark theme pair that shares")
	fmt.Println("the same accent colors), you can set a fixed one instead.")
	accent, err := readLine("Accent color (hex like #FF6AC1, ANSI 0-15, or leave blank to use your terminal's theme): ")
	if err != nil {
		return config.Config{}, err
	}
	accent = strings.TrimSpace(accent)

	cfg := config.Config{PAT: pat, BaseID: base.ID, Table: table.Name, Accent: accent}
	if err := config.Save(cfg); err != nil {
		return config.Config{}, fmt.Errorf("saving config: %w", err)
	}

	path, _ := config.Path()
	fmt.Println()
	fmt.Printf("Saved to %s\n", path)
	fmt.Println("Starting...")
	fmt.Println()

	return cfg, nil
}

// readLine reads one plain (visible) line of input.
func readLine(prompt string) (string, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

// readSecret reads a line from stdin without echoing it to the terminal,
// falling back to a plain (visible) read if stdin isn't a real terminal
// (e.g. piped input in a script or test).
func readSecret(prompt string) (string, error) {
	fmt.Print(prompt)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Println()
		return string(b), err
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

// chooseOne prints a numbered list of items and reads a selection.
func chooseOne[T any](kind string, items []T, name func(T) string) (T, error) {
	var zero T
	for i, item := range items {
		fmt.Printf("  %d. %s\n", i+1, name(item))
	}
	fmt.Printf("Choose a %s [1-%d]: ", kind, len(items))

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return zero, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(items) {
		return zero, fmt.Errorf("invalid choice %q", strings.TrimSpace(line))
	}
	return items[n-1], nil
}

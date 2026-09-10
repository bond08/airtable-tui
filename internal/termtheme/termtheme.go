// Package termtheme best-effort detects the current terminal's configured
// accent color by reading its config file directly. Ghostty, Kitty, and
// Alacritty are understood directly; for anything else, or if detection
// fails, DetectAccent returns "" and the caller falls back to its default.
package termtheme

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DetectAccent returns the current terminal's configured accent color
// (conventionally the "magenta"/palette-5 slot) as a hex string, or "" if
// it can't be determined -- either because the terminal isn't one we know
// how to read, or its config doesn't define one where we expect it.
func DetectAccent() string {
	for _, detect := range []func() string{
		detectGhostty,
		detectKitty,
		detectAlacritty,
	} {
		if hex := detect(); hex != "" {
			return hex
		}
	}
	return ""
}

var (
	ghosttyPaletteRe = regexp.MustCompile(`^palette\s*=\s*5\s*=\s*(#[0-9a-fA-F]{6})`)
	kittyColorRe     = regexp.MustCompile(`^color5\s+(#[0-9a-fA-F]{6})`)
	// Matches a "magenta" key in either TOML (`magenta = "#hex"`) or YAML
	// (`magenta: '#hex'` / `magenta: "#hex"` / `magenta: #hex`) form.
	magentaKeyRe = regexp.MustCompile(`^magenta\s*[=:]\s*['"]?(#[0-9a-fA-F]{6})`)
)

func detectGhostty() string {
	if os.Getenv("TERM_PROGRAM") != "ghostty" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".config", "ghostty")
	mainConfig := filepath.Join(dir, "config")

	// Ghostty configs can pull in another file via `config-file = <path>`
	// (relative to the config dir); many theme-switcher setups use this to
	// point at a symlink they repoint when switching themes.
	if included := findIncludeDirective(mainConfig, dir, "config-file"); included != "" {
		if hex := scanForHex(included, ghosttyPaletteRe); hex != "" {
			return hex
		}
	}
	return scanForHex(mainConfig, ghosttyPaletteRe)
}

func detectKitty() string {
	if os.Getenv("KITTY_WINDOW_ID") == "" && os.Getenv("TERM") != "xterm-kitty" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".config", "kitty")
	mainConfig := filepath.Join(dir, "kitty.conf")

	// Kitty theme switchers commonly work the same way, via an
	// `include <path>` directive pointing at a theme file.
	if included := findIncludeDirective(mainConfig, dir, "include"); included != "" {
		if hex := scanForHex(included, kittyColorRe); hex != "" {
			return hex
		}
	}
	return scanForHex(mainConfig, kittyColorRe)
}

func detectAlacritty() string {
	if os.Getenv("ALACRITTY_SOCKET") == "" && os.Getenv("ALACRITTY_LOG") == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".config", "alacritty")

	// Alacritty 0.13+ defaults to TOML; some setups still use the older
	// YAML format. Try both, and follow one level of "import" if the
	// magenta key isn't directly in the main file (a common theme-file
	// pattern, same idea as Ghostty's config-file/Kitty's include).
	for _, name := range []string{"alacritty.toml", "alacritty.yml", "alacritty.yaml"} {
		path := filepath.Join(dir, name)
		if hex := scanForHex(path, magentaKeyRe); hex != "" {
			return hex
		}
		for _, imported := range findAlacrittyImports(path, dir) {
			if hex := scanForHex(imported, magentaKeyRe); hex != "" {
				return hex
			}
		}
	}
	return ""
}

// findIncludeDirective looks for a `<directive> = <path>` or
// `<directive> <path>` line and resolves it relative to baseDir.
func findIncludeDirective(path, baseDir, directive string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, directive) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, directive))
		rest = strings.TrimPrefix(rest, "=")
		rest = strings.Trim(strings.TrimSpace(rest), `"'`)
		if rest == "" {
			continue
		}
		return filepath.Join(baseDir, rest)
	}
	return ""
}

// findAlacrittyImports extracts every quoted path from an `import = [...]`
// (TOML) or `import:` / `- path` (YAML) block, resolved relative to
// baseDir. Best-effort: a config that spreads the array across many lines
// in an unusual way may not be fully matched.
func findAlacrittyImports(path, baseDir string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range regexp.MustCompile(`"([^"]+\.(?:toml|yml|yaml))"`).FindAllStringSubmatch(string(data), -1) {
		out = append(out, filepath.Join(baseDir, m[1]))
	}
	return out
}

// scanForHex reads path line by line and returns the first regex match's
// first capture group. os.Open follows symlinks transparently, so a
// theme-switcher symlink works here with no special handling.
func scanForHex(path string, re *regexp.Regexp) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if m := re.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

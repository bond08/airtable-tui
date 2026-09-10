package termtheme

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectGhosttyDirectPalette(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TERM_PROGRAM", "ghostty")

	writeFile(t, filepath.Join(home, ".config", "ghostty", "config"), "palette = 5=#b48ead\n")

	if got := DetectAccent(); got != "#b48ead" {
		t.Errorf("DetectAccent() = %q, want #b48ead", got)
	}
}

func TestDetectGhosttyViaConfigFileDirective(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TERM_PROGRAM", "ghostty")

	writeFile(t, filepath.Join(home, ".config", "ghostty", "config"), "config-file = themes/active.conf\n")
	writeFile(t, filepath.Join(home, ".config", "ghostty", "themes", "active.conf"), "palette = 5=#ff79c6\n")

	if got := DetectAccent(); got != "#ff79c6" {
		t.Errorf("DetectAccent() = %q, want #ff79c6", got)
	}
}

func TestDetectGhosttyFollowsSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TERM_PROGRAM", "ghostty")

	writeFile(t, filepath.Join(home, ".config", "ghostty", "config"), "config-file = themes/active.conf\n")
	writeFile(t, filepath.Join(home, ".config", "ghostty", "themes", "nord.conf"), "palette = 5=#b48ead\n")

	link := filepath.Join(home, ".config", "ghostty", "themes", "active.conf")
	if err := os.Symlink(filepath.Join(home, ".config", "ghostty", "themes", "nord.conf"), link); err != nil {
		t.Fatal(err)
	}

	if got := DetectAccent(); got != "#b48ead" {
		t.Errorf("DetectAccent() via symlink = %q, want #b48ead", got)
	}

	// Repoint the symlink at a different theme -- simulates a theme
	// switcher -- and confirm detection picks up the new target.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".config", "ghostty", "themes", "dracula.conf"), "palette = 5=#ff79c6\n")
	if err := os.Symlink(filepath.Join(home, ".config", "ghostty", "themes", "dracula.conf"), link); err != nil {
		t.Fatal(err)
	}

	if got := DetectAccent(); got != "#ff79c6" {
		t.Errorf("DetectAccent() after switching symlink = %q, want #ff79c6", got)
	}
}

func TestDetectSkipsWhenNotGhostty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TERM_PROGRAM", "") // not ghostty
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("ALACRITTY_SOCKET", "")
	t.Setenv("ALACRITTY_LOG", "")

	writeFile(t, filepath.Join(home, ".config", "ghostty", "config"), "palette = 5=#b48ead\n")

	if got := DetectAccent(); got != "" {
		t.Errorf("DetectAccent() without TERM_PROGRAM=ghostty = %q, want empty (never guess)", got)
	}
}

func TestDetectKitty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("KITTY_WINDOW_ID", "1")

	writeFile(t, filepath.Join(home, ".config", "kitty", "kitty.conf"), "color5 #c4a7e7\n")

	if got := DetectAccent(); got != "#c4a7e7" {
		t.Errorf("DetectAccent() kitty = %q, want #c4a7e7", got)
	}
}

func TestDetectAlacrittyToml(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("ALACRITTY_SOCKET", "/tmp/whatever.sock")

	writeFile(t, filepath.Join(home, ".config", "alacritty", "alacritty.toml"), `
[colors.normal]
magenta = "#d699b6"
`)

	if got := DetectAccent(); got != "#d699b6" {
		t.Errorf("DetectAccent() alacritty toml = %q, want #d699b6", got)
	}
}

func TestDetectReturnsEmptyWhenNoConfigFound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TERM_PROGRAM", "ghostty")
	// No config file written at all.

	if got := DetectAccent(); got != "" {
		t.Errorf("DetectAccent() with no config file = %q, want empty", got)
	}
}

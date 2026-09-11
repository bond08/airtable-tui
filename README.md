# airtable-tui

[![CI](https://github.com/bond08/airtable-tui/actions/workflows/ci.yml/badge.svg)](https://github.com/bond08/airtable-tui/actions/workflows/ci.yml)

A terminal client for Airtable, built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).
List, filter, create, edit, and delete records; view attachments (including
inline image previews via the Kitty graphics protocol) — all without leaving
your terminal.

Works with any Airtable base and table: fields, select options, and linked
records are all read from your base's live schema, not hardcoded.

## Install

### Homebrew (macOS/Linux)

```sh
brew install bond08/tap/airtable-tui
```

### Go

```sh
go install github.com/bond08/airtable-tui/cmd/airtable-tui@latest
```

Requires Go 1.26+.

### Prebuilt binaries

Download a binary for Linux, macOS, or Windows (amd64/arm64) from the
[Releases page](https://github.com/bond08/airtable-tui/releases).

## Setup

On first run, `airtable-tui` walks you through a one-time setup:

1. Create a Personal Access Token at [airtable.com/create/tokens](https://airtable.com/create/tokens)
   with these scopes:
   - `data.records:read`
   - `data.records:write`
   - `schema.bases:read`

   Grant it access to whichever base(s) you want to use.
2. Run `airtable-tui`. It'll ask for the token, then let you pick a base and
   table from a numbered list.
3. Your choice is saved to `~/.config/airtable-tui/config.json` (readable only
   by you — see [Security](#security) below).

To switch tokens, bases, or tables later, either run `airtable-tui --reconfigure`,
or edit/delete the config file directly.

### Environment variable overrides

`AIRTABLE_PAT`, `AIRTABLE_BASE`, `AIRTABLE_TABLE`, and `AIRTABLE_ACCENT`
override the corresponding config file values if set — useful for CI,
scripting, or temporarily pointing at a different base without touching your
saved config.

### Accent color

The UI's accent color (selection highlight, borders, title badges) is derived
from your terminal's own theme by default. For Ghostty, Kitty, and Alacritty,
it's read directly from your terminal's own config file at startup (following
one level of `include`/`config-file`/`import`, so theme-switcher setups work
too); for any other terminal, it falls back to a plain ANSI color that the
terminal renders using its own currently active palette.

Either way, this is checked once at startup — switching your terminal theme
mid-session won't update it until you restart `airtable-tui`.

If it still doesn't look right — for example, a light/dark theme pair that
reuses the same colors for both, so the accent doesn't visibly change when you
switch — you can set a fixed one instead:

```sh
airtable-tui --accent "#FF6AC1"   # any hex color
airtable-tui --accent 5           # or a base-16 ANSI index (0-15)
airtable-tui --accent auto        # clear the override, go back to the default
```

This is also offered as a prompt during setup.

## Usage

Once running, switch tables anytime with `T` — there's no need to reconfigure
just to work with a different table in the same base.

| Key | Action |
|---|---|
| `↑`/`↓`, `j`/`k` | Move selection |
| `/` | Search (fuzzy filter) |
| `c` | Toggle showing only completed / hiding completed |
| `s` | Set status |
| `f` | Filter by status |
| `i` | View an attached image full-screen |
| `n` | Create a new record |
| `e` | Edit the selected record |
| `d` | Delete the selected record (with confirmation) |
| `y` | Copy the selected record's details to your clipboard |
| `T` | Switch tables |
| `r` | Refresh |
| `Q` / `Ctrl+C` | Quit |

Image previews (in the detail pane, and the full `i` view) require a terminal
that supports the Kitty graphics protocol — Ghostty, Kitty, and WezTerm all
work. Other terminals will simply show attachment filenames instead.

## Security

- Your Personal Access Token is stored only in
  `~/.config/airtable-tui/config.json`, created with `0600` permissions
  (readable/writable only by your user).
- The token is sent only to `api.airtable.com` over HTTPS, as a standard
  `Authorization: Bearer` header — never logged, never sent anywhere else.
- Treat your token like a password: anyone with it has whatever access you
  granted it when creating it. Prefer the narrowest scopes and base access
  that your use case needs.
- If a token is ever exposed (e.g. pasted somewhere public), revoke it
  immediately at [airtable.com/create/tokens](https://airtable.com/create/tokens)
  and generate a new one.

## License

MIT — see [LICENSE](LICENSE).

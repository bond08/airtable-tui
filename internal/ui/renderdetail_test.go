package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/bond08/airtable-tui/internal/airtable"
)

// newTestDetailModel builds a minimal Model with enough state to exercise
// renderDetail directly: a schema with N text fields plus one long
// (word-wrapping) field, a single record with all of them filled in, and
// that record selected in the list.
func newTestDetailModel(t *testing.T, fieldCount int, height int, withPreview bool) Model {
	t.Helper()

	fields := []airtable.Field{{ID: "fldTitle", Name: "Title", Type: "singleLineText"}}
	rec := airtable.Record{ID: "rec1", Fields: map[string]any{"Title": "Test Record"}}
	for i := 0; i < fieldCount; i++ {
		name := "Field"
		if i > 0 {
			name = "Field" + string(rune('A'+i))
		}
		fields = append(fields, airtable.Field{ID: "fld" + name, Name: name, Type: "singleLineText"})
		rec.Fields[name] = "some value"
	}
	// One deliberately long field to force word-wrapping, since that's
	// where the height-accounting bugs kept surfacing.
	fields = append(fields, airtable.Field{ID: "fldLong", Name: "Description", Type: "multilineText"})
	rec.Fields["Description"] = strings.Repeat("word ", 60)

	m := New(nil, "Test", "", "dev")
	m.width, m.height = 100, height
	m.tableSchema = airtable.Table{PrimaryFieldID: "fldTitle", Fields: fields}
	m.allRecords = []airtable.Record{rec}
	m.applyFilter()
	m.list.Select(0)
	if withPreview {
		m.sidebarPreview = "\x1b_G...fake-image-escape...\x1b\\"
	}
	return m
}

// assertPreviewNeverOverlapsText is the core invariant every past bug in
// this area violated: whatever renderDetail says previewRow is, the
// content string must actually be blank starting at that row for
// previewReservedRows rows. If any of those rows contain real text, the
// image (drawn there via cursor positioning in View) would overlap it.
func assertPreviewNeverOverlapsText(t *testing.T, content string, previewRow int) {
	t.Helper()
	if previewRow == 0 {
		t.Fatal("previewRow is 0, expected a reserved block since a preview was requested")
	}

	lines := strings.Split(content, "\n")
	// previewRow is 1-indexed from the top of the *box* (border + padding
	// included); content starts 2 rows into the box (border + top
	// padding), so within `lines` the reserved block starts at index
	// previewRow-1-2 = previewRow-3.
	startIdx := previewRow - 3
	if startIdx < 0 {
		t.Fatalf("previewRow %d maps to a negative index into content (content has %d lines)", previewRow, len(lines))
	}

	for i := startIdx; i < startIdx+previewReservedRows && i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			t.Errorf("line %d (row %d, inside the reserved image block) is not blank: %q\nfull content:\n%s",
				i, i+3, lines[i], content)
		}
	}
}

func TestRenderDetailNoOverlapAcrossFieldCounts(t *testing.T) {
	// The whole point of the bug we kept hitting: overlap only showed up
	// once a record had "enough" fields. Sweep a range of counts and
	// heights to catch that class of bug instead of testing one snapshot.
	for _, fieldCount := range []int{0, 1, 3, 8, 15, 30} {
		for _, height := range []int{20, 30, 50} {
			m := newTestDetailModel(t, fieldCount, height, true)
			content, previewRow := m.renderDetail(60)
			assertPreviewNeverOverlapsText(t, content, previewRow)
		}
	}
}

func TestRenderDetailPreviewRowMovesWithContentLength(t *testing.T) {
	short := newTestDetailModel(t, 1, 50, true)
	_, shortRow := short.renderDetail(60)

	long := newTestDetailModel(t, 15, 50, true)
	_, longRow := long.renderDetail(60)

	if longRow <= shortRow {
		t.Errorf("expected previewRow to move down for a record with more fields: short=%d long=%d", shortRow, longRow)
	}
}

func TestRenderDetailNoPreviewMeansNoReservedRow(t *testing.T) {
	m := newTestDetailModel(t, 5, 50, false)
	_, previewRow := m.renderDetail(60)
	if previewRow != 0 {
		t.Errorf("previewRow = %d, want 0 when no sidebar preview is showing", previewRow)
	}
}

func TestRenderDetailHiddenFieldsNoteWhenTooManyFields(t *testing.T) {
	// A short box with a lot of fields must hide the overflow rather than
	// silently render past the box, and say so.
	m := newTestDetailModel(t, 50, 20, true)
	content, previewRow := m.renderDetail(60)
	assertPreviewNeverOverlapsText(t, content, previewRow)
	if !strings.Contains(content, "more field(s) not shown") {
		t.Error("expected a 'more field(s) not shown' note when fields don't fit, but content has none")
	}
}

func TestLipglossHeightMatchesNewlineCount(t *testing.T) {
	// Sanity-check the assumption the whole measurement approach rests on.
	s := "a\nb\nc"
	if got := lipgloss.Height(s); got != 3 {
		t.Errorf("lipgloss.Height(%q) = %d, want 3", s, got)
	}
}

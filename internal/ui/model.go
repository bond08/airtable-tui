// Package ui implements the Bubbletea TUI: a list of Airtable records with
// keyboard navigation, status filtering, and status updates.
package ui

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bond08/airtable-tui/internal/airtable"
	"github.com/bond08/airtable-tui/internal/imgview"
)

// pollInterval is how often we re-fetch records in the background to pick
// up changes made outside the TUI (e.g. in the Airtable web app).
const pollInterval = 20 * time.Second

// defaultAccentColor is the app's signature accent, applied to selection
// highlights, borders, and title badges. Using a base-16 ANSI index (not a
// hex value) means the terminal itself renders it using whatever color its
// current theme actually assigns to that slot -- authentically
// theme-derived, though that also means it won't visibly change if a
// particular light/dark theme pair happens to reuse the same ANSI palette
// for both (as several Ghostty theme pairs do). New's accent parameter lets
// a user override this with their own fixed color when that's the case.
const defaultAccentColor = lipgloss.Color("5")

// accentColor is set once by New and used throughout the package.
var accentColor lipgloss.Color = defaultAccentColor

// appMarginX/Y reserve a small gap around the whole app so content doesn't
// sit flush against the terminal edges.
const (
	appMarginX = 1
	appMarginY = 1
)

// previewReservedRows is how many text rows renderDetail leaves blank for
// the sidebar image preview (when one is showing), after all the field
// text. renderDetail computes and returns exactly which row that blank
// space starts at (it depends on how many lines the fields took), so
// View() always draws the image into the space the text actually left
// empty for it, rather than a fixed/guessed position.
const previewReservedRows = 11

// kittyClearAll deletes every image placement currently drawn on screen.
// Kitty-protocol images are an overlay bitmap, not regular text, so a
// normal screen redraw doesn't clear them on its own -- without this, a
// previously-shown image (the full 'i' view, or a stale sidebar preview)
// keeps visually lingering even after the model has moved on.
const kittyClearAll = "\x1b_Ga=d\x1b\\"

// statusColorPalette assigns a consistent, distinct color to each status
// value (by position in the field's schema order, not by hashing the
// string), so "Completed" is always the same color across every launch and
// every list refresh -- that consistency is the point: it lets you filter
// by color at a glance instead of reading each label.
var statusColorPalette = []lipgloss.Color{
	"2",  // green
	"4",  // blue
	"5",  // magenta
	"6",  // cyan
	"9",  // bright red
	"11", // bright yellow
	"12", // bright blue
	"13", // bright magenta
	"14", // bright cyan
	"3",  // yellow
}

// item adapts an airtable.Record to the list.Item interface bubbles/list
// requires: Title(), Description(), FilterValue(). titleField is the
// table's actual primary field name (varies per table), not hardcoded, so
// switching tables shows the right thing instead of "(untitled)".
type item struct {
	record      airtable.Record
	titleField  string
	statusColor lipgloss.Color // "" means no color (table has no Status field)
}

func (i item) Title() string {
	if v, ok := i.record.Fields[i.titleField]; ok {
		return fmt.Sprintf("%v", v)
	}
	return "(untitled)"
}

func (i item) Description() string {
	status := "(no status)"
	if v, ok := i.record.Fields["Status"]; ok {
		status = fmt.Sprintf("%v", v)
	}
	if i.statusColor == "" {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(status)
	}
	return lipgloss.NewStyle().Foreground(i.statusColor).Render(status)
}

func (i item) FilterValue() string { return i.Title() }

// statusItem adapts a plain string status name to list.Item for the status
// picker overlay.
type statusItem string

func (s statusItem) Title() string       { return string(s) }
func (s statusItem) Description() string { return "" }
func (s statusItem) FilterValue() string { return string(s) }

// clearStatusLabel is the sentinel picker entry that clears the Status
// field instead of setting it to a real choice.
const clearStatusLabel = "(clear status)"

// allStatusesLabel is the sentinel picker entry that removes the status
// filter (subject still to showCompleted).
const allStatusesLabel = "(all statuses)"

// noStatusLabel filters to records with an empty Status field.
const noStatusLabel = "(no status)"

// noStatusFilterValue is filterStatus's internal marker for "no status",
// distinct from "" which means "no filter applied at all".
const noStatusFilterValue = "\x00__no_status__"

// mode tracks which "screen" the UI is showing. iota gives each constant an
// incrementing int value (modeList=0, modePicker=1, ...).
type mode int

const (
	modeList mode = iota
	modePicker
	modeImage
	modeWizard
)

// pickerPurpose distinguishes what the (shared) picker overlay does with
// the selected item, since "set status" and "filter by status" both boil
// down to "pick one status from a list" but act differently on Enter.
type pickerPurpose int

const (
	purposeSetStatus pickerPurpose = iota
	purposeFilter
	purposeSwitchTable
	purposeConfirmDelete
)

// Model is the top-level Bubbletea model.
type Model struct {
	client *airtable.Client
	table  string

	list          list.Model
	picker        list.Model
	mode          mode
	pickerPurpose pickerPurpose

	allRecords    []airtable.Record
	bgRecords     []airtable.Record // accumulates pages of a background refresh until it's complete
	statusOptions []string
	statusColors  map[string]lipgloss.Color
	showCompleted bool
	filterStatus  string         // "" means no filter
	tableSchema   airtable.Table // field order/types, for the detail pane

	allTables     map[string]airtable.Table    // by table ID, for resolving linked-field names
	linkedRecords map[string][]airtable.Record // by linked table ID

	imageContent string // rendered Kitty-protocol escape sequence for modeImage

	wizard *wizardState // non-nil while modeWizard is active

	sidebarPreview  string            // rendered preview for the current selection, if any
	needsImageClear bool              // true for exactly one frame after an image needs clearing
	previewCache    map[string]string // by record ID, so re-selecting doesn't re-fetch
	lastPreviewedID string

	width, height int

	flashMessage string // transient status note, e.g. "Copied to clipboard"

	spinner spinner.Model
	loading bool // true while a records fetch is in flight

	err error
}

// New builds the initial Model. Records and schema load via Init commands.
// accentOverride, if non-empty, replaces the terminal-derived default
// accent color -- a hex value like "#FF6AC1" or a base-16 ANSI index like
// "5" both work (see lipgloss.Color). Pass "" to keep the default.
func New(client *airtable.Client, table string, accentOverride string) Model {
	if accentOverride != "" {
		accentColor = lipgloss.Color(accentOverride)
	} else {
		accentColor = defaultAccentColor
	}

	delegate := list.NewDefaultDelegate()
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Foreground(accentColor).BorderForeground(accentColor)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.
		Foreground(accentColor).BorderForeground(accentColor)

	titleBadge := lipgloss.NewStyle().Background(accentColor).Foreground(lipgloss.Color("0")).Bold(true).Padding(0, 1)

	l := list.New(nil, delegate, 0, 0)
	l.Title = "Airtable: " + table
	l.Styles.Title = titleBadge

	p := list.New(nil, delegate, 0, 0)
	p.Styles.Title = titleBadge

	sp := spinner.New(spinner.WithSpinner(spinner.Dot))
	sp.Style = lipgloss.NewStyle().Foreground(accentColor)

	return Model{
		client:        client,
		table:         table,
		list:          l,
		picker:        p,
		spinner:       sp,
		loading:       true,
		allTables:     map[string]airtable.Table{},
		linkedRecords: map[string][]airtable.Record{},
		previewCache:  map[string]string{},
	}
}

// --- Messages: the results of async commands, delivered back into Update ---

// recordsPageMsg carries one page of records back from an in-progress
// fetch. fresh marks the first page of a fetch (the caller should replace
// rather than append). background marks the silent periodic tick refresh,
// which accumulates pages privately and only replaces the visible list
// once the whole fetch completes -- otherwise a large table's list would
// visibly shrink back down to one page's worth every ~20s while refreshing.
type recordsPageMsg struct {
	records    []airtable.Record
	nextOffset string
	fresh      bool
	background bool
	err        error
}

type schemaMsg struct {
	tables []airtable.Table
	err    error
}

type imageMsg struct {
	rendered string
	err      error
}

// previewMsg carries a rendered sidebar thumbnail back for a specific
// record, so a slow fetch that finishes after the user has since moved on
// doesn't clobber whatever's currently selected.
type previewMsg struct {
	recordID string
	rendered string
	err      error
}

type linkedRecordsMsg struct {
	tableID string
	records []airtable.Record
	err     error
}

type statusUpdatedMsg struct {
	record airtable.Record
	err    error
}

// tickMsg fires every pollInterval to trigger a background refetch.
type tickMsg time.Time

// tick schedules the next tickMsg. tea.Tick is the standard Bubbletea
// pattern for recurring work: each tickMsg handler re-issues another tick
// command, forming a self-sustaining loop for as long as the program runs.
func tick() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// fetchRecordsPage kicks off (or continues) a paginated fetch of the
// current table. Call with offset="" and fresh=true to start a new fetch;
// the returned recordsPageMsg's nextOffset tells the caller whether to
// call this again to continue, which is how the page-by-page chain
// advances (see the recordsPageMsg handler in Update).
func (m Model) fetchRecordsPage(offset string, fresh, background bool) tea.Cmd {
	return func() tea.Msg {
		records, next, err := m.client.ListRecordsPage(context.Background(), m.table, offset)
		return recordsPageMsg{records: records, nextOffset: next, fresh: fresh, background: background, err: err}
	}
}

func (m Model) fetchSchema() tea.Cmd {
	return func() tea.Msg {
		tables, err := m.client.ListTables(context.Background())
		return schemaMsg{tables: tables, err: err}
	}
}

// fetchLinkedRecords loads every record of a linked table, so we can
// resolve the record IDs stored in a multipleRecordLinks field to their
// human-readable titles. Airtable's API accepts a table ID anywhere it
// accepts a table name, so tableID works directly here.
func (m Model) fetchLinkedRecords(tableID string) tea.Cmd {
	return func() tea.Msg {
		records, err := m.client.ListRecords(context.Background(), tableID)
		return linkedRecordsMsg{tableID: tableID, records: records, err: err}
	}
}

// firstImageAttachmentURL looks across every multipleAttachments field on
// rec for the first attachment whose MIME type starts with "image/".
// firstImageAttachmentURL finds the first image attachment on rec. When
// preferThumbnail is true it returns Airtable's own pre-generated "large"
// thumbnail (a few KB, already resized) instead of the full original file
// -- much faster to fetch/decode/render, and plenty of resolution for a
// small sidebar preview. The full 'i' view still asks for the original.
func (m Model) firstImageAttachmentURL(rec airtable.Record, preferThumbnail bool) (string, bool) {
	for _, field := range m.tableSchema.Fields {
		if field.Type != "multipleAttachments" {
			continue
		}
		atts, ok := rec.Fields[field.Name].([]any)
		if !ok {
			continue
		}
		for _, a := range atts {
			att, ok := a.(map[string]any)
			if !ok {
				continue
			}
			mime, _ := att["type"].(string)
			url, _ := att["url"].(string)
			if url == "" || !strings.HasPrefix(mime, "image/") {
				continue
			}
			if preferThumbnail {
				if thumbs, ok := att["thumbnails"].(map[string]any); ok {
					if large, ok := thumbs["large"].(map[string]any); ok {
						if thumbURL, ok := large["url"].(string); ok && thumbURL != "" {
							return thumbURL, true
						}
					}
				}
			}
			return url, true
		}
	}
	return "", false
}

// loadImage fetches and renders an attachment URL as a tea.Cmd, sized to
// fit the current terminal window.
func (m Model) loadImage(url string) tea.Cmd {
	colsInt, rowsInt := m.width-4, m.height-4
	if colsInt < 1 {
		colsInt = 20
	}
	if rowsInt < 1 {
		rowsInt = 20
	}
	cols, rows := uint32(colsInt), uint32(rowsInt)
	return func() tea.Msg {
		data, err := imgview.Fetch(url)
		if err != nil {
			return imageMsg{err: err}
		}
		rendered, err := imgview.Render(data, cols, rows)
		return imageMsg{rendered: rendered, err: err}
	}
}

// loadPreview fetches and renders a small thumbnail for the sidebar.
func (m Model) loadPreview(recordID, url string) tea.Cmd {
	return func() tea.Msg {
		data, err := imgview.Fetch(url)
		if err != nil {
			return previewMsg{recordID: recordID, err: err}
		}
		rendered, err := imgview.Render(data, 24, 10)
		return previewMsg{recordID: recordID, rendered: rendered, err: err}
	}
}

// maybePreviewCmd checks whether the selection has moved to a record we
// haven't already previewed, and if so either serves it from cache or
// switchTable changes which table the whole UI is pointed at. m.allTables
// already holds every table's schema (fetched once at startup), so this
// only needs to reset per-table state and refetch records -- no new schema
// call required, though we do fetch any linked table this table references
// that we haven't cached yet.
func (m *Model) switchTable(tableName string) tea.Cmd {
	m.table = tableName
	for _, t := range m.allTables {
		if t.Name == tableName {
			m.tableSchema = t
		}
	}

	m.allRecords = nil
	m.statusOptions = nil
	m.filterStatus = ""
	m.showCompleted = false
	m.sidebarPreview = ""
	m.lastPreviewedID = ""
	m.list.Title = "Airtable: " + m.table
	m.list.SetItems(nil)

	for _, f := range m.tableSchema.Fields {
		if f.Name == "Status" {
			for _, c := range f.Options.Choices {
				m.statusOptions = append(m.statusOptions, c.Name)
			}
		}
	}
	m.statusColors = buildStatusColors(m.statusOptions)

	m.loading = true
	cmds := []tea.Cmd{m.fetchRecordsPage("", true, false), m.spinner.Tick}
	for _, f := range m.tableSchema.Fields {
		if f.Type == "multipleRecordLinks" && f.Options.LinkedTableID != "" {
			if _, cached := m.linkedRecords[f.Options.LinkedTableID]; !cached {
				cmds = append(cmds, m.fetchLinkedRecords(f.Options.LinkedTableID))
			}
		}
	}
	return tea.Batch(cmds...)
}

// returns a command to fetch+render it. Called after anything that could
// change which record is selected (navigation, list reloads).
func (m *Model) maybePreviewCmd() tea.Cmd {
	// Only clear anything if there's actually an image on screen right now
	// that needs to go away. A full tea.ClearScreen (blank-then-redraw)
	// visibly flashes, so instead we set a one-frame flag that makes
	// View() send just the lightweight Kitty "delete images" command
	// (which doesn't touch text, so no flash) and then immediately reset
	// it via clearImageOnceCmd -- see the needsImageClear field and the
	// imageCleared message handler.
	hadImage := m.sidebarPreview != ""

	rec, ok := m.selectedRecord()
	if !ok {
		m.sidebarPreview = ""
		m.lastPreviewedID = ""
		if hadImage {
			m.needsImageClear = true
			return clearImageOnceCmd()
		}
		return nil
	}
	if rec.ID == m.lastPreviewedID {
		return nil
	}
	m.lastPreviewedID = rec.ID

	url, ok := m.firstImageAttachmentURL(rec, true)
	if !ok {
		m.sidebarPreview = ""
		if hadImage {
			m.needsImageClear = true
			return clearImageOnceCmd()
		}
		return nil
	}
	if cached, ok := m.previewCache[rec.ID]; ok {
		m.sidebarPreview = cached
		if hadImage {
			m.needsImageClear = true
			return clearImageOnceCmd()
		}
		return nil
	}
	m.sidebarPreview = ""
	if hadImage {
		m.needsImageClear = true
		return tea.Batch(clearImageOnceCmd(), m.loadPreview(rec.ID, url))
	}
	return m.loadPreview(rec.ID, url)
}

// imageClearedMsg turns needsImageClear back off after exactly one frame.
type imageClearedMsg struct{}

func clearImageOnceCmd() tea.Cmd {
	return func() tea.Msg { return imageClearedMsg{} }
}

// updateStatus sets the Status field to newStatus, or clears it entirely
// when newStatus is nil.
func (m Model) updateStatus(recordID string, newStatus any) tea.Cmd {
	return func() tea.Msg {
		rec, err := m.client.UpdateRecord(context.Background(), m.table, recordID, map[string]any{
			"Status": newStatus,
		})
		return statusUpdatedMsg{record: rec, err: err}
	}
}

// deletedMsg carries the result of deleting a record.
type deletedMsg struct {
	recordID string
	err      error
}

func (m Model) deleteRecord(recordID string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.DeleteRecord(context.Background(), m.table, recordID)
		return deletedMsg{recordID: recordID, err: err}
	}
}

// Init runs once when the program starts. tea.Batch runs both commands
// concurrently; both results arrive as separate messages to Update.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchRecordsPage("", true, false), m.fetchSchema(), tick(), m.spinner.Tick)
}

// buildStatusColors assigns each status a color by its position in the
// field's schema order (wrapping around the palette if there are more
// statuses than colors), so the mapping is stable across refreshes.
func buildStatusColors(options []string) map[string]lipgloss.Color {
	colors := make(map[string]lipgloss.Color, len(options))
	for i, s := range options {
		colors[s] = statusColorPalette[i%len(statusColorPalette)]
	}
	return colors
}

// isCompleted reports whether a status value counts as "done" for the
// default hide-completed filter.
func isCompleted(status string) bool {
	return status == "Completed" || status == "Done"
}

// applyFilter rebuilds the visible list items from allRecords, honoring
// filterStatus (exact match, if set) and otherwise showCompleted.
func (m *Model) applyFilter() {
	// Airtable's createdTime is an ISO 8601 UTC timestamp, which sorts
	// correctly as a plain string -- no need to parse it into time.Time.
	// Newest first; re-sorting here (rather than once after fetch) keeps
	// order correct after a create/edit mutates m.allRecords in place.
	sort.Slice(m.allRecords, func(i, j int) bool {
		return m.allRecords[i].CreatedTime > m.allRecords[j].CreatedTime
	})

	items := make([]list.Item, 0, len(m.allRecords))
	for _, rec := range m.allRecords {
		status, _ := rec.Fields["Status"].(string)
		switch {
		case m.filterStatus == noStatusFilterValue:
			if status != "" {
				continue
			}
		case m.filterStatus != "":
			if status != m.filterStatus {
				continue
			}
		case m.showCompleted != isCompleted(status):
			// showCompleted=false: keep only non-completed.
			// showCompleted=true:  keep only completed.
			continue
		}
		items = append(items, item{
			record:      rec,
			titleField:  m.tableSchema.PrimaryFieldName(),
			statusColor: m.statusColors[status],
		})
	}
	m.list.SetItems(items)

	title := "Airtable: " + m.table
	switch {
	case m.filterStatus == noStatusFilterValue:
		title += "  [filter: " + noStatusLabel + "]"
	case m.filterStatus != "":
		title += "  [filter: " + m.filterStatus + "]"
	case m.showCompleted:
		title += "  [completed only]"
	}
	m.list.Title = title
}

// selectedRecord returns the record currently highlighted in the list, if any.
func (m Model) selectedRecord() (airtable.Record, bool) {
	sel, ok := m.list.SelectedItem().(item)
	if !ok {
		return airtable.Record{}, false
	}
	return sel.record, true
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		// Reserve appMarginX/Y around the whole app; everything below sizes
		// itself against m.width/m.height, so this is the one place that
		// needs to know about the margin -- View() adds it back in when
		// composing the final frame.
		m.width = msg.Width - appMarginX*2
		m.height = msg.Height - appMarginY
		listWidth := m.width * 3 / 5
		m.list.SetSize(listWidth, m.height)
		m.picker.SetSize(m.width/2, m.height/2)
		return m, nil

	case recordsPageMsg:
		if msg.err != nil {
			m.loading = false
			m.err = msg.err
			return m, nil
		}
		m.err = nil // a successful page clears any earlier transient error

		if msg.background {
			// Accumulate silently; don't touch the visible list (or the
			// loading spinner) until every page of this refresh is in, so
			// a large table's list never visibly shrinks back down to one
			// page's worth mid-poll.
			if msg.fresh {
				m.bgRecords = nil
			}
			m.bgRecords = append(m.bgRecords, msg.records...)
			if msg.nextOffset != "" {
				return m, m.fetchRecordsPage(msg.nextOffset, false, true)
			}
			m.allRecords = m.bgRecords
			m.bgRecords = nil
			m.applyFilter()
			return m, m.maybePreviewCmd()
		}

		// Visible fetch (initial load, manual refresh, table switch):
		// show each page as it arrives instead of waiting for a large
		// table's entire record set.
		if msg.fresh {
			m.allRecords = msg.records
		} else {
			m.allRecords = append(m.allRecords, msg.records...)
		}
		m.applyFilter()
		if msg.nextOffset == "" {
			m.loading = false
			return m, m.maybePreviewCmd()
		}
		return m, m.fetchRecordsPage(msg.nextOffset, false, false)

	case schemaMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		for _, t := range msg.tables {
			m.allTables[t.ID] = t
			if t.Name == m.table {
				m.tableSchema = t
			}
		}
		for _, f := range m.tableSchema.Fields {
			if f.Name == "Status" {
				m.statusOptions = nil
				for _, c := range f.Options.Choices {
					m.statusOptions = append(m.statusOptions, c.Name)
				}
			}
		}
		m.statusColors = buildStatusColors(m.statusOptions)
		// Kick off a fetch for every linked table this table references,
		// so the detail pane can show names instead of raw record IDs.
		var cmds []tea.Cmd
		for _, f := range m.tableSchema.Fields {
			if f.Type == "multipleRecordLinks" && f.Options.LinkedTableID != "" {
				cmds = append(cmds, m.fetchLinkedRecords(f.Options.LinkedTableID))
			}
		}
		return m, tea.Batch(cmds...)

	case previewMsg:
		if msg.err == nil {
			m.previewCache[msg.recordID] = msg.rendered
			if msg.recordID == m.lastPreviewedID {
				m.sidebarPreview = msg.rendered
			}
		}
		// Silently drop preview failures (e.g. unsupported format) --
		// this is a best-effort thumbnail, not worth surfacing as m.err.
		return m, nil

	case imageMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.imageContent = msg.rendered
		m.mode = modeImage
		return m, nil

	case linkedRecordsMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.linkedRecords[msg.tableID] = msg.records
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.fetchRecordsPage("", true, true), tick())

	case spinner.TickMsg:
		if !m.loading {
			return m, nil // stop the animation loop once nothing's in flight
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case imageClearedMsg:
		m.needsImageClear = false
		return m, nil

	case statusUpdatedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		for i, rec := range m.allRecords {
			if rec.ID == msg.record.ID {
				m.allRecords[i] = msg.record
			}
		}
		m.applyFilter()
		m.mode = modeList
		return m, m.maybePreviewCmd()

	case wizardSubmitMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		if msg.purpose == wizardCreate {
			m.allRecords = append(m.allRecords, msg.record)
		} else {
			for i, rec := range m.allRecords {
				if rec.ID == msg.record.ID {
					m.allRecords[i] = msg.record
				}
			}
		}
		m.wizard = nil
		m.mode = modeList
		m.applyFilter()
		return m, nil

	case deletedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		for i, rec := range m.allRecords {
			if rec.ID == msg.recordID {
				m.allRecords = append(m.allRecords[:i], m.allRecords[i+1:]...)
				break
			}
		}
		m.applyFilter()
		return m, nil

	case copiedMsg:
		m.flashMessage = "Copied to clipboard"
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return clearFlashMsg{} })

	case clearFlashMsg:
		m.flashMessage = ""
		return m, nil

	case tea.KeyMsg:
		if m.err != nil {
			// Errors are a dismissible banner, not a dead end: the first
			// keypress after one just clears it, rather than the whole UI
			// staying stuck showing only the error forever.
			m.err = nil
			return m, nil
		}
		if m.mode == modeWizard {
			return m.updateWizard(msg)
		}
		if m.mode == modeImage {
			m.mode = modeList
			m.imageContent = ""
			// Kitty images sit in their own overlay layer that a normal
			// redraw doesn't necessarily touch; force a full terminal
			// repaint in addition to our own explicit clear command, so
			// the full-screen image reliably disappears.
			return m, tea.ClearScreen
		}
		if m.mode == modePicker {
			return m.updatePicker(msg)
		}
		return m.updateList(msg)
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While the user is actively typing into the list's built-in fuzzy
	// filter (started with "/"), don't let our single-letter shortcuts
	// steal keystrokes -- let every key through to the list untouched.
	if m.list.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "Q", "ctrl+c":
		return m, tea.Quit

	case "c":
		m.showCompleted = !m.showCompleted
		m.applyFilter()
		return m, nil

	case "r":
		m.loading = true
		return m, tea.Batch(m.fetchRecordsPage("", true, false), m.spinner.Tick)

	case "i":
		rec, ok := m.selectedRecord()
		if !ok {
			return m, nil
		}
		url, ok := m.firstImageAttachmentURL(rec, false)
		if !ok {
			return m, nil
		}
		return m, m.loadImage(url)

	case "n":
		return m, m.startWizard(wizardCreate, "", nil)

	case "e":
		rec, ok := m.selectedRecord()
		if !ok {
			return m, nil
		}
		return m, m.startWizard(wizardEdit, rec.ID, rec.Fields)

	case "s":
		if _, ok := m.selectedRecord(); !ok {
			return m, nil
		}
		items := make([]list.Item, 0, len(m.statusOptions)+1)
		items = append(items, statusItem(clearStatusLabel))
		for _, s := range m.statusOptions {
			items = append(items, statusItem(s))
		}
		m.picker.Title = "Set status"
		m.picker.SetItems(items)
		m.pickerPurpose = purposeSetStatus
		m.mode = modePicker
		return m, nil

	case "f":
		items := make([]list.Item, 0, len(m.statusOptions)+2)
		items = append(items, statusItem(allStatusesLabel), statusItem(noStatusLabel))
		for _, s := range m.statusOptions {
			items = append(items, statusItem(s))
		}
		m.picker.Title = "Filter by status"
		m.picker.SetItems(items)
		m.pickerPurpose = purposeFilter
		m.mode = modePicker
		return m, nil

	case "T":
		names := make([]string, 0, len(m.allTables))
		for _, t := range m.allTables {
			names = append(names, t.Name)
		}
		sort.Strings(names)
		items := make([]list.Item, len(names))
		for i, n := range names {
			items[i] = statusItem(n)
		}
		m.picker.Title = "Switch table"
		m.picker.SetItems(items)
		m.pickerPurpose = purposeSwitchTable
		m.mode = modePicker
		return m, nil

	case "d":
		rec, ok := m.selectedRecord()
		if !ok {
			return m, nil
		}
		titleField := m.tableSchema.PrimaryFieldName()
		title := item{record: rec, titleField: titleField}.Title()
		items := []list.Item{statusItem("Cancel"), statusItem("Delete: " + title)}
		m.picker.Title = "Delete this task?"
		m.picker.SetItems(items)
		m.pickerPurpose = purposeConfirmDelete
		m.mode = modePicker
		return m, nil

	case "y":
		rec, ok := m.selectedRecord()
		if !ok {
			return m, nil
		}
		return m, copyToClipboard(m.plainDetailText(rec))

	case "esc":
		// Esc never quits the app -- only q/ctrl+c do. This is an explicit
		// no-op at the main screen so it can't be mistaken for a quit
		// shortcut; inside a picker/wizard, Esc still cancels that overlay
		// and returns here, which is a different, intentional behavior.
		return m, nil
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	previewCmd := m.maybePreviewCmd()
	return m, tea.Batch(cmd, previewCmd)
}

func (m Model) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.picker.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.picker, cmd = m.picker.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "esc":
		m.mode = modeList
		return m, nil

	case "enter":
		sel, selOk := m.picker.SelectedItem().(statusItem)
		if !selOk {
			m.mode = modeList
			return m, nil
		}
		m.mode = modeList

		if m.pickerPurpose == purposeFilter {
			switch string(sel) {
			case allStatusesLabel:
				m.filterStatus = ""
			case noStatusLabel:
				m.filterStatus = noStatusFilterValue
			default:
				m.filterStatus = string(sel)
			}
			m.applyFilter()
			return m, nil
		}

		if m.pickerPurpose == purposeSwitchTable {
			return m, m.switchTable(string(sel))
		}

		if m.pickerPurpose == purposeConfirmDelete {
			if !strings.HasPrefix(string(sel), "Delete:") {
				return m, nil // "Cancel" selected
			}
			rec, ok := m.selectedRecord()
			if !ok {
				return m, nil
			}
			return m, m.deleteRecord(rec.ID)
		}

		rec, ok := m.selectedRecord()
		if !ok {
			return m, nil
		}
		if string(sel) == clearStatusLabel {
			return m, m.updateStatus(rec.ID, nil)
		}
		return m, m.updateStatus(rec.ID, string(sel))
	}

	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(msg)
	return m, cmd
}

// linkedRecordTitle resolves one linked record ID to its primary-field
// value, falling back to the raw ID if the linked table/record isn't
// loaded yet or can't be found.
func (m Model) linkedRecordTitle(tableID, recordID string) string {
	table, ok := m.allTables[tableID]
	if !ok {
		return recordID
	}
	titleField := table.PrimaryFieldName()
	for _, rec := range m.linkedRecords[tableID] {
		if rec.ID == recordID {
			if v, ok := rec.Fields[titleField]; ok {
				return fmt.Sprintf("%v", v)
			}
			return recordID
		}
	}
	return recordID
}

// formatFieldValue renders one field's raw JSON value as a human-readable
// string, based on the field's schema type.
func (m Model) formatFieldValue(field airtable.Field, val any) string {
	switch field.Type {
	case "multipleRecordLinks":
		ids, ok := val.([]any)
		if !ok {
			return fmt.Sprintf("%v", val)
		}
		names := make([]string, len(ids))
		for i, id := range ids {
			idStr, _ := id.(string)
			names[i] = m.linkedRecordTitle(field.Options.LinkedTableID, idStr)
		}
		return strings.Join(names, ", ")

	case "multipleAttachments":
		atts, ok := val.([]any)
		if !ok {
			return fmt.Sprintf("%v", val)
		}
		names := make([]string, 0, len(atts))
		for _, a := range atts {
			if m, ok := a.(map[string]any); ok {
				if fn, ok := m["filename"].(string); ok {
					names = append(names, fn)
					continue
				}
			}
			names = append(names, "(attachment)")
		}
		return strings.Join(names, ", ")

	case "aiText":
		obj, ok := val.(map[string]any)
		if !ok {
			return fmt.Sprintf("%v", val)
		}
		if state, _ := obj["state"].(string); state == "empty" || obj["value"] == nil {
			return "(pending)"
		}
		return fmt.Sprintf("%v", obj["value"])

	default:
		// Formula/rollup fields that error out come back as
		// {"error": "#ERROR!"} or {"specialValue": "NaN"/"Infinity"}
		// instead of a plain value -- show that message instead of raw
		// Go map syntax.
		if obj, ok := val.(map[string]any); ok {
			if e, ok := obj["error"].(string); ok {
				return e
			}
			if s, ok := obj["specialValue"].(string); ok {
				return s
			}
		}
		return fmt.Sprintf("%v", val)
	}
}

// renderDetail formats every field of the selected record, in the table's
// real schema order (map iteration order in Go is randomized, so we can't
// just range over rec.Fields directly and get a stable layout).
// renderDetail lays out every field of the selected record, in the table's
// real schema order (map iteration order in Go is randomized, so we can't
// just range over rec.Fields directly and get a stable layout). contentWidth
// is the usable width inside the detail box, used to wrap long values and
// to size the divider under the title.
// renderDetail also returns the screen row (relative to the top of the
// detail box) where it left blank space reserved for the sidebar image
// preview, if one is showing -- that space comes after all the field text,
// so its position isn't known until we've finished writing the fields.
// plainDetailText builds a copy-paste-friendly plain-text version of a
// record's fields -- no ANSI styling, unlike renderDetail's output, since
// that's meant for the terminal display, not a clipboard payload.
func (m Model) plainDetailText(rec airtable.Record) string {
	titleField := m.tableSchema.PrimaryFieldName()
	var b strings.Builder
	b.WriteString(item{record: rec, titleField: titleField}.Title())
	b.WriteString("\n\n")
	for _, field := range m.tableSchema.Fields {
		if field.Name == titleField ||
			field.Type == "multipleAttachments" ||
			field.Type == "count" ||
			field.Type == "aiText" {
			continue
		}
		val, present := rec.Fields[field.Name]
		if !present {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", field.Name, m.formatFieldValue(field, val))
	}
	return b.String()
}

// copyToClipboard sends text to the system clipboard via OSC 52, an escape
// sequence the terminal itself intercepts (supported by Ghostty, Kitty,
// WezTerm, iTerm2, and others) -- this works locally or over SSH, and
// doesn't depend on any clipboard tool/library being installed.
func copyToClipboard(text string) tea.Cmd {
	return func() tea.Msg {
		payload := base64.StdEncoding.EncodeToString([]byte(text))
		fmt.Fprintf(os.Stdout, "\x1b]52;c;%s\x07", payload)
		return copiedMsg{}
	}
}

// copiedMsg confirms a clipboard copy so we can flash a brief status note.
type copiedMsg struct{}

// clearFlashMsg clears the transient status note a few seconds after it's shown.
type clearFlashMsg struct{}

func (m Model) renderDetail(contentWidth int) (content string, previewRow int) {
	rec, ok := m.selectedRecord()
	if !ok {
		return "(nothing selected)", 0
	}
	if contentWidth < 10 {
		contentWidth = 10
	}

	titleStyle := lipgloss.NewStyle().Bold(true)
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	valueStyle := lipgloss.NewStyle().Width(contentWidth)
	dividerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	titleField := m.tableSchema.PrimaryFieldName()

	var b strings.Builder
	b.WriteString(titleStyle.Render(item{record: rec, titleField: titleField}.Title()))
	b.WriteString("\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", contentWidth)))
	b.WriteString("\n\n")

	// Budget how many text lines actually fit in the box: total height minus
	// top+bottom border (2) and top+bottom padding (2). If a preview is
	// showing, reserve room for it up front so we never write field text
	// into rows the image is about to be drawn over -- instead we stop
	// listing fields once we'd run out of room, which keeps the reserved
	// blank space (and thus the image) always within the visible viewport.
	maxLines := m.height - 4
	linesUsed := 3 // title + divider + blank line already written above
	linesBudgetForFields := maxLines
	if m.sidebarPreview != "" {
		linesBudgetForFields -= previewReservedRows + 1 // +1 for the blank separator line
	}

	first := true
	hiddenFields := 0
	for _, field := range m.tableSchema.Fields {
		if field.Name == titleField ||
			field.Type == "multipleAttachments" ||
			field.Type == "count" ||
			field.Type == "aiText" {
			continue
		}
		val, present := rec.Fields[field.Name]
		if !present {
			continue
		}

		formatted := m.formatFieldValue(field, val)
		multiline := len(formatted) > 40 || strings.Contains(formatted, "\n")
		lineCost := 1
		if multiline {
			lineCost = 2
		}
		if maxLines > 0 && linesUsed+lineCost > linesBudgetForFields {
			hiddenFields++
			continue
		}

		if !first {
			b.WriteString("\n")
		}
		first = false

		label := labelStyle.Render(field.Name + ":")
		// Long or multi-line values get their own line below the label
		// (Description, Progress Notes, ...); short ones stay inline.
		if multiline {
			b.WriteString(label)
			b.WriteString("\n")
			b.WriteString(valueStyle.Render(formatted))
		} else {
			b.WriteString(label)
			b.WriteString(" ")
			b.WriteString(formatted)
		}
		b.WriteString("\n")
		linesUsed += lineCost
	}

	if hiddenFields > 0 {
		b.WriteString(dividerStyle.Render(fmt.Sprintf("… %d more field(s) not shown (resize terminal to see)", hiddenFields)))
		b.WriteString("\n")
	}

	if m.sidebarPreview != "" {
		b.WriteString("\n")
		// previewRow is 1-indexed from the top of the detail box: 1 line
		// for the border + 1 for top padding + however many lines of text
		// we've written so far + 1 to land on the row right after them.
		// Since we already reserved room for it above, this is guaranteed
		// to land within the visible viewport -- no separate clamp needed.
		previewRow = 2 + strings.Count(b.String(), "\n") + 1
		// Leave this space blank so nothing else renders on top of where
		// View() will draw the actual image via cursor positioning.
		b.WriteString(strings.Repeat("\n", previewReservedRows))
	}

	return b.String(), previewRow
}

// indentBlock prepends n spaces to every line of s, for the left/right
// margin. Plain string manipulation rather than a lipgloss margin, since
// the caller composes this with raw ANSI escapes afterward (the image
// preview) that lipgloss's width-aware styling would otherwise risk
// corrupting.
func indentBlock(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = pad + line
	}
	return strings.Join(lines, "\n")
}

// helpEntries is the list of keybindings shown in the footer, grouped
// loosely by purpose (view/filter, edit, app-level).
var helpEntries = []struct{ key, label string }{
	{"c", "completed"}, {"s", "status"}, {"f", "filter"}, {"i", "image"},
	{"n", "new"}, {"e", "edit"}, {"d", "delete"}, {"y", "copy"},
	{"T", "table"}, {"r", "refresh"}, {"Q", "quit"},
}

// renderHelpBar builds the footer: each key in the accent color, its label
// dimmed, separated by a subtle middle dot -- quieter and easier to scan
// than a long run-on line. A transient flash message (e.g. after a copy)
// replaces it briefly instead of competing alongside it.
func (m Model) renderHelpBar() string {
	if m.err != nil {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true).
			Render(fmt.Sprintf("Error: %v", m.err)) +
			lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("  (press any key to dismiss)")
	}
	if m.flashMessage != "" {
		return lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(m.flashMessage)
	}

	keyStyle := lipgloss.NewStyle().Foreground(accentColor).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	sep := labelStyle.Render(" · ")

	prefix := ""
	if m.loading {
		prefix = m.spinner.View() + " " + labelStyle.Render("loading") + sep
	}

	parts := make([]string, len(helpEntries))
	for i, e := range helpEntries {
		parts[i] = keyStyle.Render(e.key) + labelStyle.Render(" "+e.label)
	}
	return prefix + strings.Join(parts, sep)
}

func (m Model) View() string {
	if m.mode == modeImage {
		// Kitty graphics escape sequences aren't normal text -- passing
		// them through lipgloss (which measures/pads by rune width) would
		// corrupt them, so render this mode as a raw, unstyled string.
		// Clear first so a lingering sidebar preview doesn't show through.
		return kittyClearAll + m.imageContent + "\n(press any key to return)"
	}

	if m.mode == modeWizard {
		return m.renderWizard()
	}

	detailWidth := m.width - lipgloss.Width(m.list.View())
	const detailPaddingX = 2
	detailContent, previewRow := m.renderDetail(detailWidth - 2 - detailPaddingX*2)
	detail := lipgloss.NewStyle().
		Width(detailWidth-2).
		Height(m.height-2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accentColor).
		Padding(1, detailPaddingX).
		Render(detailContent)

	base := lipgloss.JoinHorizontal(lipgloss.Top, m.list.View(), detail)

	topMargin := strings.Repeat("\n", appMarginY)

	if m.mode == modePicker {
		overlay := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accentColor).
			Render(m.picker.View())
		composed := lipgloss.Place(
			lipgloss.Width(base), lipgloss.Height(base),
			lipgloss.Center, lipgloss.Center,
			overlay,
		)
		return topMargin + indentBlock(composed, appMarginX)
	}

	help := "\n" + m.renderHelpBar()
	out := topMargin + indentBlock(base+help, appMarginX)

	if m.sidebarPreview != "" {
		// Kitty escapes anchor at the cursor when drawn, so we can't embed
		// this inside the lipgloss-bordered detail box (it would corrupt
		// that box's width math). Instead, jump the cursor to the blank
		// region renderDetail reserved for it (after all the field text)
		// and draw it there, after everything else has already been
		// printed. Offsets account for the top/left margin added above.
		//
		// The clear-all is sent only on frames that actually draw an
		// image, not on every frame (spinner ticks, keystrokes, streamed
		// list updates, ...), which was the earlier source of visible
		// flicker during rapid redraws like the loading spinner.
		listWidth := lipgloss.Width(m.list.View())
		row, col := previewRow+appMarginY, listWidth+4+appMarginX
		out += kittyClearAll + fmt.Sprintf("\x1b[%d;%dH%s", row, col, m.sidebarPreview)
	} else if m.needsImageClear {
		// Navigated away from a record that had an image, to one that
		// doesn't: nothing new to draw, but the old one needs to
		// disappear. This is a lightweight Kitty command (only touches
		// the image layer, not text), not a full tea.ClearScreen repaint
		// -- that's what avoided a visible flash here.
		out += kittyClearAll
	}

	return out
}

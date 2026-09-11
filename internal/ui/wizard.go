// Wizard implements the create/edit flow: a sequential, one-field-at-a-time
// form that walks every editable field on the table, using a widget suited
// to that field's schema type, then shows a review screen before submitting.
package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bond08/airtable-tui/internal/airtable"
)

// editableTypes are the field types the wizard knows how to render. Any
// other type (formula, aiText, count, multipleAttachments, rollup, ...) is
// computed/unsupported and stays out of the wizard entirely.
var editableTypes = map[string]bool{
	"singleLineText":      true,
	"multilineText":       true,
	"email":               true,
	"url":                 true,
	"phoneNumber":         true,
	"number":              true,
	"percent":             true,
	"currency":            true,
	"date":                true,
	"dateTime":            true,
	"singleSelect":        true,
	"multipleRecordLinks": true,
	"checkbox":            true,
	"rating":              true, // edited as a plain number (the star count)
	"duration":            true, // edited as a plain number (seconds)
}

func editableFields(table airtable.Table) []airtable.Field {
	var out []airtable.Field
	for _, f := range table.Fields {
		if editableTypes[f.Type] {
			out = append(out, f)
		}
	}
	return out
}

// wizardPurpose distinguishes creating a new record from editing an
// existing one -- same step flow, different final API call and starting
// values.
type wizardPurpose int

const (
	wizardCreate wizardPurpose = iota
	wizardEdit
)

// stepKind identifies which widget the current step is showing.
type stepKind int

const (
	stepText stepKind = iota
	stepMultiline
	stepSelect
	stepLink
	stepCheckbox
	stepReview
)

// wizardState holds everything about an in-progress create/edit flow.
type wizardState struct {
	purpose  wizardPurpose
	recordID string // only meaningful for wizardEdit

	fields []airtable.Field
	step   int
	values map[string]any // accumulated across all steps, keyed by field name

	kind stepKind

	textInput  textinput.Model
	textArea   textarea.Model
	pickList   list.Model      // used for both stepSelect and stepLink
	linkChosen map[string]bool // by linked record ID, for stepLink's checkboxes
}

// wizardChoiceItem adapts a plain label to list.Item for the singleSelect
// picker step.
type wizardChoiceItem string

func (w wizardChoiceItem) Title() string       { return string(w) }
func (w wizardChoiceItem) Description() string { return "" }
func (w wizardChoiceItem) FilterValue() string { return string(w) }

// wizardLinkItem adapts a linked record to list.Item for the
// multipleRecordLinks picker step, showing a checkbox prefix.
type wizardLinkItem struct {
	recordID string
	label    string
	chosen   bool
}

func (w wizardLinkItem) Title() string {
	mark := "[ ]"
	if w.chosen {
		mark = "[x]"
	}
	return mark + " " + w.label
}
func (w wizardLinkItem) Description() string { return "" }
func (w wizardLinkItem) FilterValue() string { return w.Title() }

// startWizard begins a create or edit flow and configures the first step.
func (m *Model) startWizard(purpose wizardPurpose, recordID string, initial map[string]any) tea.Cmd {
	fields := editableFields(m.tableSchema)
	if initial == nil {
		initial = map[string]any{}
	}
	m.wizard = &wizardState{
		purpose:  purpose,
		recordID: recordID,
		fields:   fields,
		step:     0,
		values:   initial,
	}
	m.mode = modeWizard
	return tea.Batch(m.clearImageCmd(), m.setupWizardStep())
}

// setupWizardStep configures the widget for the current step, or switches
// to the review screen once every field has been visited.
func (m *Model) setupWizardStep() tea.Cmd {
	w := m.wizard
	if w.step >= len(w.fields) {
		w.kind = stepReview
		m.buildWizardReview()
		return nil
	}

	field := w.fields[w.step]
	existing := w.values[field.Name]

	switch field.Type {
	case "singleSelect":
		w.kind = stepSelect
		choices := []string{"(leave empty)"}
		for _, c := range field.Options.Choices {
			choices = append(choices, c.Name)
		}
		items := make([]list.Item, len(choices))
		for i, c := range choices {
			items[i] = wizardChoiceItem(c)
		}
		w.pickList = list.New(items, list.NewDefaultDelegate(), m.width/2, m.height/2)
		w.pickList.Title = field.Name

	case "multipleRecordLinks":
		w.kind = stepLink
		w.linkChosen = map[string]bool{}
		if ids, ok := existing.([]any); ok {
			for _, id := range ids {
				if s, ok := id.(string); ok {
					w.linkChosen[s] = true
				}
			}
		}
		linked := m.linkedRecords[field.Options.LinkedTableID]
		items := make([]list.Item, len(linked))
		for i, rec := range linked {
			items[i] = wizardLinkItem{
				recordID: rec.ID,
				label:    m.linkedRecordTitle(field.Options.LinkedTableID, rec.ID),
				chosen:   w.linkChosen[rec.ID],
			}
		}
		w.pickList = list.New(items, list.NewDefaultDelegate(), m.width/2, m.height/2)
		w.pickList.Title = field.Name + " (space=toggle, enter=confirm)"

	case "checkbox":
		w.kind = stepCheckbox
		checked, _ := existing.(bool)
		items := []list.Item{wizardChoiceItem("false"), wizardChoiceItem("true")}
		w.pickList = list.New(items, list.NewDefaultDelegate(), m.width/2, m.height/2)
		w.pickList.Title = field.Name
		if checked {
			w.pickList.Select(1)
		} else {
			w.pickList.Select(0)
		}

	case "multilineText":
		w.kind = stepMultiline
		w.textArea = textarea.New()
		w.textArea.SetWidth(m.width * 3 / 5)
		w.textArea.SetHeight(m.height / 3)
		if s, ok := existing.(string); ok {
			w.textArea.SetValue(s)
		}
		w.textArea.Focus()

	default: // singleLineText, email, url, phoneNumber, number, percent, currency, date, dateTime
		w.kind = stepText
		w.textInput = textinput.New()
		w.textInput.Width = m.width * 3 / 5
		if existing != nil {
			w.textInput.SetValue(fmt.Sprintf("%v", existing))
		}
		w.textInput.Focus()
	}
	return nil
}

// buildWizardReview renders the accumulated values as a summary list.
func (m *Model) buildWizardReview() {
	w := m.wizard
	items := make([]list.Item, len(w.fields))
	for i, f := range w.fields {
		items[i] = wizardChoiceItem(fmt.Sprintf("%s: %s", f.Name, m.formatFieldValue(f, w.values[f.Name])))
	}
	w.pickList = list.New(items, list.NewDefaultDelegate(), m.width*3/5, m.height*3/5)
	title := "Review new task"
	if w.purpose == wizardEdit {
		title = "Review changes"
	}
	w.pickList.Title = title
}

// wizardSubmitMsg carries the result of creating or updating a record.
type wizardSubmitMsg struct {
	purpose wizardPurpose
	record  airtable.Record
	err     error
}

// submitWizard builds the fields payload and fires the right API call.
func (m Model) submitWizard() tea.Cmd {
	w := m.wizard
	fields := map[string]any{}
	for _, f := range w.fields {
		v := w.values[f.Name]
		if v == nil && w.purpose == wizardCreate {
			continue // omit empty fields on create; Airtable defaults them
		}
		fields[f.Name] = v
	}

	return func() tea.Msg {
		var rec airtable.Record
		var err error
		if w.purpose == wizardCreate {
			rec, err = m.client.CreateRecord(context.Background(), m.table, fields)
		} else {
			rec, err = m.client.UpdateRecord(context.Background(), m.table, w.recordID, fields)
		}
		return wizardSubmitMsg{purpose: w.purpose, record: rec, err: err}
	}
}

func (m Model) updateWizard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	w := m.wizard

	if msg.String() == "esc" {
		m.wizard = nil
		m.mode = modeList
		return m, nil
	}

	switch w.kind {
	case stepReview:
		switch msg.String() {
		case "b":
			if w.step > 0 {
				w.step--
			}
			return m, m.setupWizardStep()
		case "enter":
			return m, m.submitWizard()
		}
		var cmd tea.Cmd
		w.pickList, cmd = w.pickList.Update(msg)
		return m, cmd

	case stepSelect:
		switch msg.String() {
		case "enter":
			field := w.fields[w.step]
			sel, _ := w.pickList.SelectedItem().(wizardChoiceItem)
			if string(sel) == "(leave empty)" {
				w.values[field.Name] = nil
			} else {
				w.values[field.Name] = string(sel)
			}
			w.step++
			return m, m.setupWizardStep()
		}
		var cmd tea.Cmd
		w.pickList, cmd = w.pickList.Update(msg)
		return m, cmd

	case stepCheckbox:
		switch msg.String() {
		case "enter":
			field := w.fields[w.step]
			sel, _ := w.pickList.SelectedItem().(wizardChoiceItem)
			w.values[field.Name] = string(sel) == "true"
			w.step++
			return m, m.setupWizardStep()
		}
		var cmd tea.Cmd
		w.pickList, cmd = w.pickList.Update(msg)
		return m, cmd

	case stepLink:
		switch msg.String() {
		case " ":
			field := w.fields[w.step]
			linked := m.linkedRecords[field.Options.LinkedTableID]
			idx := w.pickList.Index()
			if idx >= 0 && idx < len(linked) {
				rec := linked[idx]
				w.linkChosen[rec.ID] = !w.linkChosen[rec.ID]
				items := make([]list.Item, len(linked))
				for i, r := range linked {
					items[i] = wizardLinkItem{
						recordID: r.ID,
						label:    m.linkedRecordTitle(field.Options.LinkedTableID, r.ID),
						chosen:   w.linkChosen[r.ID],
					}
				}
				w.pickList.SetItems(items)
			}
			return m, nil
		case "enter":
			field := w.fields[w.step]
			var ids []any
			for id, chosen := range w.linkChosen {
				if chosen {
					ids = append(ids, id)
				}
			}
			w.values[field.Name] = ids
			w.step++
			return m, m.setupWizardStep()
		}
		var cmd tea.Cmd
		w.pickList, cmd = w.pickList.Update(msg)
		return m, cmd

	case stepMultiline:
		switch msg.String() {
		case "ctrl+s":
			field := w.fields[w.step]
			val := strings.TrimSpace(w.textArea.Value())
			if val == "" {
				w.values[field.Name] = nil
			} else {
				w.values[field.Name] = val
			}
			w.step++
			return m, m.setupWizardStep()
		}
		var cmd tea.Cmd
		w.textArea, cmd = w.textArea.Update(msg)
		return m, cmd

	default: // stepText
		switch msg.String() {
		case "enter":
			field := w.fields[w.step]
			val := strings.TrimSpace(w.textInput.Value())
			if val == "" {
				w.values[field.Name] = nil
			} else if isNumericFieldType(field.Type) {
				w.values[field.Name] = val // API accepts numeric strings; Airtable coerces
			} else {
				w.values[field.Name] = val
			}
			w.step++
			return m, m.setupWizardStep()
		}
		var cmd tea.Cmd
		w.textInput, cmd = w.textInput.Update(msg)
		return m, cmd
	}
}

func isNumericFieldType(t string) bool {
	return t == "number" || t == "percent" || t == "currency" || t == "rating" || t == "duration"
}

func (m Model) renderWizard() string {
	w := m.wizard
	if w == nil {
		return ""
	}

	var content string
	switch w.kind {
	case stepReview:
		content = w.pickList.View() + "\nenter: submit  b: back  esc: cancel"
	case stepSelect, stepLink, stepCheckbox:
		content = w.pickList.View()
	case stepMultiline:
		label := w.fields[w.step].Name + " (ctrl+s: save, esc: cancel, enter: newline)"
		content = label + "\n\n" + w.textArea.View()
	default:
		label := w.fields[w.step].Name + " (enter: save, esc: cancel)"
		content = label + "\n\n" + w.textInput.View()
	}

	if m.err != nil {
		content += "\n\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true).
			Render(fmt.Sprintf("Error: %v", m.err)) +
			lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("  (press any key to dismiss)")
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.accentColor).
		Padding(1, 2).
		Render(content)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

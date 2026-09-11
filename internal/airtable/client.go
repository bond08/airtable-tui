// Package airtable is a minimal client for the parts of the Airtable REST
// API this tool needs: listing/creating/updating records, and reading a
// table's schema (fields, select options, linked tables).
package airtable

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"time"
)

const apiBase = "https://api.airtable.com/v0"

// httpTimeout bounds every request -- without this, a hung connection
// (dead network, unresponsive proxy) blocks the calling goroutine forever
// instead of surfacing as a normal, recoverable error.
const httpTimeout = 15 * time.Second

// maxRetries is how many times a 429 (rate limited) response is retried,
// with exponential backoff, before giving up and returning the error.
// Airtable's limit is 5 requests/second per base; this is what keeps a
// burst of requests (e.g. fetching several linked tables at once) from
// surfacing as a hard failure to the user.
const maxRetries = 3

// Client talks to the Airtable API using a Personal Access Token.
// Fields are unexported (lowercase) since callers only need the methods.
type Client struct {
	pat        string
	baseID     string
	httpClient *http.Client
}

// New creates a Client. baseID identifies which Airtable base to hit;
// individual methods take a table name/ID as a parameter.
func New(pat, baseID string) *Client {
	return &Client{
		pat:        pat,
		baseID:     baseID,
		httpClient: &http.Client{Timeout: httpTimeout},
	}
}

// Record mirrors Airtable's JSON record shape. map[string]any is Go's
// equivalent of "arbitrary JSON object" -- fields vary per table/record,
// so we can't use a fixed struct here.
type Record struct {
	ID          string         `json:"id"`
	CreatedTime string         `json:"createdTime,omitempty"`
	Fields      map[string]any `json:"fields"`
}

type listRecordsResponse struct {
	Records []Record `json:"records"`
	Offset  string   `json:"offset"`
}

// apiError captures Airtable's error body shape for better messages.
type apiError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// do sends an HTTP request with auth headers and decodes either the
// success payload (into out) or an error.
func (c *Client) do(ctx context.Context, method, fullURL string, body any, out any) error {
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		bodyBytes = b
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleepWithContext(ctx, backoffDelay(attempt)); err != nil {
				return err
			}
		}

		var reqBody io.Reader
		if bodyBytes != nil {
			reqBody = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.pat)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("%s %s: %w", method, fullURL, err)
		}

		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("airtable API %d: rate limited", resp.StatusCode)
			if attempt < maxRetries {
				continue
			}
			return lastErr
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			var apiErr apiError
			if json.Unmarshal(data, &apiErr) == nil && apiErr.Error.Message != "" {
				return fmt.Errorf("airtable API %d (%s): %s", resp.StatusCode, apiErr.Error.Type, apiErr.Error.Message)
			}
			return fmt.Errorf("airtable API %d: %s", resp.StatusCode, string(data))
		}

		if out == nil {
			return nil
		}
		return json.Unmarshal(data, out)
	}
	return lastErr
}

// backoffUnit is the base delay backoffDelay scales exponentially from.
// A var, not a const, so tests can shrink it.
var backoffUnit = time.Second

// backoffDelay returns an exponentially increasing delay (1s, 2s, 4s, ...)
// with a little jitter, for the given retry attempt (1-indexed).

func backoffDelay(attempt int) time.Duration {
	base := time.Duration(1<<uint(attempt-1)) * backoffUnit
	jitter := time.Duration(rand.Intn(25)) * backoffUnit / 100 // up to ~25% extra
	return base + jitter
}

// sleepWithContext waits for d, or returns ctx's error early if it's
// canceled first.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ListRecordsPage fetches one page of records (Airtable returns up to 100
// per page). Pass "" as offset for the first page; keep passing back the
// returned nextOffset until it comes back "", meaning there are no more
// pages.
func (c *Client) ListRecordsPage(ctx context.Context, table, offset string) (records []Record, nextOffset string, err error) {
	params := url.Values{}
	if offset != "" {
		params.Set("offset", offset)
	}
	fullURL := fmt.Sprintf("%s/%s/%s?%s", apiBase, c.baseID, url.PathEscape(table), params.Encode())

	var page listRecordsResponse
	if err := c.do(ctx, http.MethodGet, fullURL, nil, &page); err != nil {
		return nil, "", err
	}
	return page.Records, page.Offset, nil
}

// ListRecords fetches every record in a table, following pagination until
// exhausted. Prefer ListRecordsPage when results should stream to a user.
func (c *Client) ListRecords(ctx context.Context, table string) ([]Record, error) {
	var all []Record
	offset := ""
	for {
		page, next, err := c.ListRecordsPage(ctx, table, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if next == "" {
			return all, nil
		}
		offset = next
	}
}

// GetRecord fetches a single record by ID.
func (c *Client) GetRecord(ctx context.Context, table, id string) (Record, error) {
	var rec Record
	fullURL := fmt.Sprintf("%s/%s/%s/%s", apiBase, c.baseID, url.PathEscape(table), id)
	err := c.do(ctx, http.MethodGet, fullURL, nil, &rec)
	return rec, err
}

// CreateRecord creates a record with the given fields.
func (c *Client) CreateRecord(ctx context.Context, table string, fields map[string]any) (Record, error) {
	var rec Record
	fullURL := fmt.Sprintf("%s/%s/%s", apiBase, c.baseID, url.PathEscape(table))
	body := map[string]any{"fields": fields}
	err := c.do(ctx, http.MethodPost, fullURL, body, &rec)
	return rec, err
}

// UpdateRecord partially updates (PATCH) a record's fields.
func (c *Client) UpdateRecord(ctx context.Context, table, id string, fields map[string]any) (Record, error) {
	var rec Record
	fullURL := fmt.Sprintf("%s/%s/%s/%s", apiBase, c.baseID, url.PathEscape(table), id)
	body := map[string]any{"fields": fields}
	err := c.do(ctx, http.MethodPatch, fullURL, body, &rec)
	return rec, err
}

// SelectChoice is one option of a singleSelect/multipleSelect field.
type SelectChoice struct {
	Name string `json:"name"`
}

// Field describes one column in a table's schema.
type Field struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Options struct {
		Choices       []SelectChoice `json:"choices"`
		LinkedTableID string         `json:"linkedTableId"`
	} `json:"options"`
}

// Table describes a table's schema: its ID, fields (and, via Field, their
// select options / linked-table info where applicable).
type Table struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	PrimaryFieldID string  `json:"primaryFieldId"`
	Fields         []Field `json:"fields"`
}

// PrimaryFieldName returns the name of the table's primary/title field
// (falling back to the first field if the schema omits primaryFieldId).
func (t Table) PrimaryFieldName() string {
	for _, f := range t.Fields {
		if f.ID == t.PrimaryFieldID {
			return f.Name
		}
	}
	if len(t.Fields) > 0 {
		return t.Fields[0].Name
	}
	return ""
}

type listTablesResponse struct {
	Tables []Table `json:"tables"`
}

// Base describes one Airtable base accessible to the current token.
type Base struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type listBasesResponse struct {
	Bases  []Base `json:"bases"`
	Offset string `json:"offset"`
}

// ListBases fetches every base the client's token can access, across all
// pages. It doesn't need c.baseID -- this is what a setup/onboarding flow
// calls before a base has even been chosen.
func (c *Client) ListBases(ctx context.Context) ([]Base, error) {
	var all []Base
	offset := ""
	for {
		fullURL := apiBase + "/meta/bases"
		if offset != "" {
			fullURL += "?offset=" + url.QueryEscape(offset)
		}
		var resp listBasesResponse
		if err := c.do(ctx, http.MethodGet, fullURL, nil, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Bases...)
		if resp.Offset == "" {
			break
		}
		offset = resp.Offset
	}
	return all, nil
}

// ListTables fetches the schema for every table in the base.
func (c *Client) ListTables(ctx context.Context) ([]Table, error) {
	fullURL := fmt.Sprintf("%s/meta/bases/%s/tables", apiBase, c.baseID)
	var resp listTablesResponse
	if err := c.do(ctx, http.MethodGet, fullURL, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Tables, nil
}

// GetTableSchema fetches the schema for a single table by name.
func (c *Client) GetTableSchema(ctx context.Context, tableName string) (Table, error) {
	tables, err := c.ListTables(ctx)
	if err != nil {
		return Table{}, err
	}
	for _, t := range tables {
		if t.Name == tableName {
			return t, nil
		}
	}
	return Table{}, fmt.Errorf("table %q not found", tableName)
}

// DeleteRecord deletes a record by ID.
func (c *Client) DeleteRecord(ctx context.Context, table, id string) error {
	fullURL := fmt.Sprintf("%s/%s/%s/%s", apiBase, c.baseID, url.PathEscape(table), id)
	return c.do(ctx, http.MethodDelete, fullURL, nil, nil)
}

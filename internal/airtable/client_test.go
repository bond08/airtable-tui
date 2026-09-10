package airtable

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := New("test-pat", "appTEST")
	// Point the client at the test server instead of the real API. apiBase
	// is a package-level const, so tests override it via httptest's own
	// client base URL by swapping the client's transport target -- simplest
	// is a small helper client wired to hit srv.URL directly.
	c.httpClient = srv.Client()
	return c, srv
}

// Since apiBase is a compile-time const pointing at the real API, tests
// build full URLs by hand against the httptest server rather than calling
// the higher-level methods that hardcode apiBase. This still exercises the
// exact same do() logic (auth header, retry, error decoding) the real
// methods rely on.
func doTestRequest(t *testing.T, c *Client, srv *httptest.Server, method, path string, body, out any) error {
	t.Helper()
	return c.do(context.Background(), method, srv.URL+path, body, out)
}

func TestListRecordsPagination(t *testing.T) {
	page := 0
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-pat" {
			t.Errorf("Authorization header = %q, want Bearer test-pat", got)
		}
		page++
		w.Header().Set("Content-Type", "application/json")
		if page == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"records": []map[string]any{{"id": "rec1", "fields": map[string]any{"Name": "A"}}},
				"offset":  "page2",
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"records": []map[string]any{{"id": "rec2", "fields": map[string]any{"Name": "B"}}},
		})
	})

	var all []Record
	offset := ""
	for {
		var resp listRecordsResponse
		params := ""
		if offset != "" {
			params = "?offset=" + offset
		}
		if err := doTestRequest(t, c, srv, http.MethodGet, "/records"+params, nil, &resp); err != nil {
			t.Fatalf("request: %v", err)
		}
		all = append(all, resp.Records...)
		if resp.Offset == "" {
			break
		}
		offset = resp.Offset
	}

	if len(all) != 2 || all[0].ID != "rec1" || all[1].ID != "rec2" {
		t.Errorf("got %+v, want two records rec1 then rec2", all)
	}
	if page != 2 {
		t.Errorf("server hit %d times, want 2 (one per page)", page)
	}
}

func TestDoDecodesAPIError(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"type": "AUTHENTICATION_REQUIRED", "message": "Invalid token"},
		})
	})

	err := doTestRequest(t, c, srv, http.MethodGet, "/whatever", nil, nil)
	if err == nil {
		t.Fatal("want error for 401 response, got nil")
	}
	if !strings.Contains(err.Error(), "Invalid token") {
		t.Errorf("error %q does not surface Airtable's own message", err.Error())
	}
	if strings.Contains(err.Error(), "test-pat") {
		t.Error("error message must never contain the token")
	}
}

// withFastBackoff shrinks the retry backoff unit for the duration of a
// test, so retry tests don't spend real seconds sleeping.
func withFastBackoff(t *testing.T) {
	t.Helper()
	orig := backoffUnit
	backoffUnit = 10 * time.Millisecond
	t.Cleanup(func() { backoffUnit = orig })
}

func TestDoRetriesOn429ThenSucceeds(t *testing.T) {
	withFastBackoff(t)
	attempts := 0
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	start := time.Now()
	var out map[string]any
	err := doTestRequest(t, c, srv, http.MethodGet, "/x", nil, &out)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected eventual success after retries, got error: %v", err)
	}
	if attempts != 3 {
		t.Errorf("server hit %d times, want 3 (2 failures + 1 success)", attempts)
	}
	if out["ok"] != true {
		t.Errorf("decoded body = %v, want ok:true", out)
	}
	// Backoff is 1 unit then 2 units (plus jitter) between attempts -- sanity
	// check it actually waited rather than hammering the server instantly.
	if elapsed < 2*10*time.Millisecond {
		t.Errorf("elapsed = %v, expected backoff delays to add up to at least ~20ms", elapsed)
	}
}

func TestDoGivesUpAfterMaxRetries(t *testing.T) {
	withFastBackoff(t)
	attempts := 0
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
	})

	err := doTestRequest(t, c, srv, http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatal("want error after exhausting retries, got nil")
	}
	if attempts != maxRetries+1 {
		t.Errorf("server hit %d times, want %d (initial + %d retries)", attempts, maxRetries+1, maxRetries)
	}
}

func TestCreateRecordSendsFieldsAndDecodesResponse(t *testing.T) {
	var gotBody map[string]any
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":     "recNEW",
			"fields": map[string]any{"Name": "hello"},
		})
	})

	var rec Record
	err := doTestRequest(t, c, srv, http.MethodPost, "/records",
		map[string]any{"fields": map[string]any{"Name": "hello"}}, &rec)
	if err != nil {
		t.Fatalf("CreateRecord-equivalent request: %v", err)
	}
	if rec.ID != "recNEW" {
		t.Errorf("rec.ID = %q, want recNEW", rec.ID)
	}
	fields, _ := gotBody["fields"].(map[string]any)
	if fields["Name"] != "hello" {
		t.Errorf("server received fields = %v, want Name:hello", gotBody)
	}
}

func TestPrimaryFieldName(t *testing.T) {
	table := Table{
		PrimaryFieldID: "fld2",
		Fields: []Field{
			{ID: "fld1", Name: "First"},
			{ID: "fld2", Name: "Second"},
		},
	}
	if got := table.PrimaryFieldName(); got != "Second" {
		t.Errorf("PrimaryFieldName() = %q, want Second", got)
	}

	// Falls back to the first field when primaryFieldId doesn't match
	// anything (schema omitted it, or it's stale).
	table.PrimaryFieldID = "does-not-exist"
	if got := table.PrimaryFieldName(); got != "First" {
		t.Errorf("PrimaryFieldName() fallback = %q, want First", got)
	}
}

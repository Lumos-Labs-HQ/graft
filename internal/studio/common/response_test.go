package common

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── JSON response helpers ─────────────────────────────────────────────────────

func TestJSON_Success(t *testing.T) {
	w := httptest.NewRecorder()
	JSON(w, map[string]string{"key": "value"})

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !resp.Success {
		t.Error("Success should be true")
	}
}

func TestJSONMessage(t *testing.T) {
	w := httptest.NewRecorder()
	JSONMessage(w, "operation complete")

	var resp Response
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Success || resp.Message != "operation complete" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestJSONError(t *testing.T) {
	w := httptest.NewRecorder()
	JSONError(w, http.StatusBadRequest, "bad input")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	var resp Response
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Success {
		t.Error("Success should be false for error")
	}
	if resp.Message != "bad input" {
		t.Errorf("Message = %q, want 'bad input'", resp.Message)
	}
}

func TestJSONMap(t *testing.T) {
	w := httptest.NewRecorder()
	JSONMap(w, Map{"count": 42})

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var result map[string]any
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["count"] == nil {
		t.Error("count key missing from response")
	}
}

func TestJSONRaw(t *testing.T) {
	w := httptest.NewRecorder()
	JSONRaw(w, []string{"a", "b", "c"})

	var result []string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("result = %v, want [a b c]", result)
	}
}

// ── ParseJSON ─────────────────────────────────────────────────────────────────

func TestParseJSON(t *testing.T) {
	body := `{"name":"Alice","age":30}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	var target map[string]any
	if err := ParseJSON(r, &target); err != nil {
		t.Fatalf("ParseJSON error: %v", err)
	}
	if target["name"] != "Alice" {
		t.Errorf("name = %v, want Alice", target["name"])
	}
}

func TestParseJSON_Invalid(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not json"))
	var target map[string]any
	if err := ParseJSON(r, &target); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// ── Query helper ──────────────────────────────────────────────────────────────

func TestQuery_Present(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?page=3", nil)
	if got := Query(r, "page", "1"); got != "3" {
		t.Errorf("Query(page) = %q, want 3", got)
	}
}

func TestQuery_Missing_UsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := Query(r, "page", "1"); got != "1" {
		t.Errorf("Query(missing) = %q, want 1", got)
	}
}

func TestQuery_Empty_UsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?page=", nil)
	if got := Query(r, "page", "1"); got != "1" {
		t.Errorf("Query(empty) = %q, want 1", got)
	}
}

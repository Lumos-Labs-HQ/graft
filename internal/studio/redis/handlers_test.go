package redis

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Lumos-Labs-HQ/flash/internal/studio/common"
)

func TestHandlerPattern_MissingKey_Returns400(t *testing.T) {
	// Mirrors handleGetKey when key param is empty.
	w := httptest.NewRecorder()
	common.JSONError(w, http.StatusBadRequest, "key is required")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "key is required") {
		t.Errorf("body = %q", w.Body.String())
	}
}

func TestHandlerPattern_Success_Returns200(t *testing.T) {
	w := httptest.NewRecorder()
	common.JSON(w, map[string]any{"size": 42})

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestQuery_DefaultCursor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/keys", nil)
	cursor := common.Query(r, "cursor", "0")
	if cursor != "0" {
		t.Errorf("cursor = %q, want 0", cursor)
	}
}

func TestQuery_CustomPattern(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/keys?pattern=user:*", nil)
	pattern := common.Query(r, "pattern", "*")
	if pattern != "user:*" {
		t.Errorf("pattern = %q, want user:*", pattern)
	}
}

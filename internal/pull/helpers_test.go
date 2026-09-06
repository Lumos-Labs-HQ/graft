package pull

import (
	"strings"
	"testing"
)

// ── compareTableSQL ───────────────────────────────────────────────────────────

func newService() *Service {
	return &Service{}
}

func TestCompareTableSQL_Equal(t *testing.T) {
	s := newService()
	sql := `CREATE TABLE users (id SERIAL PRIMARY KEY, email TEXT NOT NULL);`
	if !s.compareTableSQL(sql, sql) {
		t.Error("identical SQL should be equal")
	}
}

func TestCompareTableSQL_WhitespaceDifference(t *testing.T) {
	s := newService()
	// Extra spaces between tokens (not adjacent to parens) should normalize away.
	a := `CREATE TABLE  users (id  SERIAL  PRIMARY KEY, email  TEXT  NOT NULL);`
	b := `CREATE TABLE users (id SERIAL PRIMARY KEY, email TEXT NOT NULL);`
	if !s.compareTableSQL(a, b) {
		t.Error("whitespace-only difference should be equal")
	}
}

func TestCompareTableSQL_CaseDifference(t *testing.T) {
	s := newService()
	a := `CREATE TABLE users (id SERIAL PRIMARY KEY);`
	b := `create table users (id serial primary key);`
	if !s.compareTableSQL(a, b) {
		t.Error("case-only difference should be equal")
	}
}

func TestCompareTableSQL_Different(t *testing.T) {
	s := newService()
	a := `CREATE TABLE users (id SERIAL PRIMARY KEY);`
	b := `CREATE TABLE users (id SERIAL PRIMARY KEY, email TEXT);`
	if s.compareTableSQL(a, b) {
		t.Error("structurally different SQL should not be equal")
	}
}

// ── extractTableSQL ───────────────────────────────────────────────────────────

func TestExtractTableSQL_Found(t *testing.T) {
	s := newService()
	content := `
-- some comment
CREATE TABLE users (
	id SERIAL PRIMARY KEY,
	email TEXT NOT NULL
);

CREATE TABLE posts (id SERIAL PRIMARY KEY);
`
	got := s.extractTableSQL(content, "users")
	if got == "" {
		t.Fatal("expected non-empty SQL for users table")
	}
	if !strings.Contains(got, "users") {
		t.Errorf("extracted SQL missing 'users': %q", got)
	}
	if strings.Contains(got, "posts") {
		t.Errorf("extracted SQL should not contain 'posts': %q", got)
	}
}

func TestExtractTableSQL_NotFound(t *testing.T) {
	s := newService()
	content := `CREATE TABLE users (id SERIAL PRIMARY KEY);`
	got := s.extractTableSQL(content, "nonexistent")
	if got != "" {
		t.Errorf("expected empty string for missing table, got %q", got)
	}
}

// ── replaceTableInContent ─────────────────────────────────────────────────────

func TestReplaceTableInContent(t *testing.T) {
	s := newService()
	content := `CREATE TABLE users (id SERIAL PRIMARY KEY);
CREATE TABLE posts (id SERIAL PRIMARY KEY);`

	newSQL := `CREATE TABLE users (id SERIAL PRIMARY KEY, email TEXT NOT NULL);`
	result := s.replaceTableInContent(content, "users", newSQL)

	if !strings.Contains(result, "email TEXT NOT NULL") {
		t.Errorf("replacement not applied: %q", result)
	}
	if !strings.Contains(result, "posts") {
		t.Errorf("posts table should be preserved: %q", result)
	}
}

// ── isFileCommentedOut ────────────────────────────────────────────────────────

func TestIsFileCommentedOut_AllComments(t *testing.T) {
	s := newService()
	content := `-- This is commented out
-- Another comment
-- CREATE TABLE users (id INT);`
	if !s.isFileCommentedOut(content) {
		t.Error("all-comment file should be considered commented out")
	}
}

func TestIsFileCommentedOut_HasCode(t *testing.T) {
	s := newService()
	content := `-- comment
CREATE TABLE users (id INT);`
	if s.isFileCommentedOut(content) {
		t.Error("file with code should not be considered commented out")
	}
}

func TestIsFileCommentedOut_Empty(t *testing.T) {
	s := newService()
	if !s.isFileCommentedOut("") {
		t.Error("empty file should be considered commented out")
	}
}

// ── commentOutFile ────────────────────────────────────────────────────────────

func TestCommentOutFile(t *testing.T) {
	s := newService()
	content := `CREATE TABLE users (id SERIAL PRIMARY KEY);`
	result := s.commentOutFile(content, "users")

	if !strings.Contains(result, "TABLE DROPPED") {
		t.Error("commented file should contain TABLE DROPPED header")
	}
	// Every non-empty line should start with --
	for line := range strings.SplitSeq(result, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "--") {
			t.Errorf("line not commented out: %q", line)
		}
	}
}

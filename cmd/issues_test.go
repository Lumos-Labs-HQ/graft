//go:build plugin_core || dev

package cmd

import (
	"strings"
	"testing"
)

// ── validateIssueInput — the "actually a real issue" guard ────────────────────

// A well-formed report of each kind must pass.
func TestValidateIssueInput_Valid(t *testing.T) {
	cases := []struct {
		name                                  string
		kind, title, body, repro, exp, actual string
	}{
		{
			name:  "bug with repro",
			kind:  "bug",
			title: "gen: RETURNING dropped for SQLite inserts",
			body:  "flash gen produces an insert helper that never returns the row.",
			repro: "1. flash init --sqlite  2. add INSERT ... RETURNING :one  3. flash gen",
		},
		{
			name:   "bug with expected+actual",
			kind:   "bug",
			title:  "studio TTL badge never ticks down",
			body:   "The Redis studio TTL badge renders once and then stops counting down.",
			exp:    "the badge decrements every second",
			actual: "the badge is frozen at its initial value",
		},
		{
			name:  "feature needs no repro",
			kind:  "feature",
			title: "support composite cache keys in dep purging",
			body:  "It would help to purge composite-key caches by a partial key prefix.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validateIssueInput(c.kind, c.title, c.body, c.repro, c.exp, c.actual); err != nil {
				t.Errorf("validateIssueInput rejected a valid report: %v", err)
			}
		})
	}
}

// Malformed / low-effort reports must be rejected, naming the offending field.
func TestValidateIssueInput_Rejects(t *testing.T) {
	longBody := "this body is definitely long enough to pass the length check easily"
	cases := []struct {
		name                                  string
		kind, title, body, repro, exp, actual string
		wantSubstr                            string
	}{
		{"empty title", "bug", "", longBody, "some repro steps here", "", "", "title"},
		{"short title", "bug", "too short", longBody, "some repro steps here", "", "", "title"},
		{"junk title", "bug", "it doesn't work", longBody, "some repro steps here", "", "", "descriptive"},
		{"empty body", "bug", "a perfectly good descriptive title", "", "some repro steps here", "", "", "description"},
		{"short body", "bug", "a perfectly good descriptive title", "too short", "some repro steps here", "", "", "description"},
		{"bug missing repro", "bug", "a perfectly good descriptive title", longBody, "", "", "", "reproduction"},
		{"bug expected only", "bug", "a perfectly good descriptive title", longBody, "", "only expected given", "", "reproduction"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateIssueInput(c.kind, c.title, c.body, c.repro, c.exp, c.actual)
			if err == nil {
				t.Fatalf("validateIssueInput accepted an invalid report (%s)", c.name)
			}
			if !strings.Contains(err.Error(), c.wantSubstr) {
				t.Errorf("error %q does not mention %q", err.Error(), c.wantSubstr)
			}
		})
	}
}

// ── prefixIssueTitle ──────────────────────────────────────────────────────────

func TestPrefixIssueTitle(t *testing.T) {
	if got := prefixIssueTitle("[Bug]", "something broke"); got != "[Bug] something broke" {
		t.Errorf("prefixIssueTitle = %q, want %q", got, "[Bug] something broke")
	}
	// An existing bracketed prefix is preserved (not doubled).
	if got := prefixIssueTitle("[Bug]", "[Feature] add a thing"); got != "[Feature] add a thing" {
		t.Errorf("prefixIssueTitle should preserve existing prefix, got %q", got)
	}
}

// ── mergeIssueLabels ──────────────────────────────────────────────────────────

func TestMergeIssueLabels(t *testing.T) {
	got := mergeIssueLabels("bug", []string{"cache", "bug", "", "cache", "studio"})
	want := []string{"bug", "cache", "studio"}
	if len(got) != len(want) {
		t.Fatalf("mergeIssueLabels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mergeIssueLabels[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// ── composeIssueBody ──────────────────────────────────────────────────────────

func TestComposeIssueBody_Sections(t *testing.T) {
	body := composeIssueBody("bug", "the description text", "step one then step two", "should work", "did not work")
	for _, want := range []string{
		"## Description", "the description text",
		"## Steps to Reproduce", "step one then step two",
		"## Expected behavior", "should work",
		"## Actual behavior", "did not work",
		"## Environment",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("composeIssueBody missing %q\n---\n%s", want, body)
		}
	}
}

// Optional sections are omitted when their input is empty.
func TestComposeIssueBody_OmitsEmptySections(t *testing.T) {
	body := composeIssueBody("feature", "just a description", "", "", "")
	if strings.Contains(body, "## Steps to Reproduce") {
		t.Errorf("composeIssueBody should omit Steps to Reproduce when repro is empty:\n%s", body)
	}
	if strings.Contains(body, "## Expected behavior") {
		t.Errorf("composeIssueBody should omit Expected behavior when empty:\n%s", body)
	}
}

// The auto-collected environment table always carries the diagnostics that make
// a report reproducible.
func TestIssueEnvironmentTable(t *testing.T) {
	table := issueEnvironmentTable()
	for _, want := range []string{"Flash version", "OS / arch", "Go runtime", "DB provider", "Cache"} {
		if !strings.Contains(table, want) {
			t.Errorf("issueEnvironmentTable missing row %q:\n%s", want, table)
		}
	}
}

// ── newIssueURL ───────────────────────────────────────────────────────────────

func TestNewIssueURL(t *testing.T) {
	u := newIssueURL("[Bug] a b", "body text", []string{"bug", "cache"})
	if !strings.HasPrefix(u, "https://github.com/"+flashIssuesRepo+"/issues/new?") {
		t.Errorf("newIssueURL wrong base: %s", u)
	}
	// Values must be query-escaped (space -> + or %20, never a raw space).
	if strings.Contains(u, "[Bug] a b") {
		t.Errorf("newIssueURL did not escape the title: %s", u)
	}
	if !strings.Contains(u, "labels=bug%2Ccache") {
		t.Errorf("newIssueURL missing escaped labels: %s", u)
	}
}

// ── issueKinds ────────────────────────────────────────────────────────────────

func TestIssueKinds(t *testing.T) {
	cases := []struct {
		kind, label, prefix string
	}{
		{"bug", "bug", "[Bug]"},
		{"feature", "enhancement", "[Feature]"},
		{"question", "question", "[Question]"},
		{"docs", "documentation", "[Docs]"},
	}
	for _, c := range cases {
		ik, ok := issueKinds[c.kind]
		if !ok {
			t.Errorf("issueKinds missing %q", c.kind)
			continue
		}
		if ik.label != c.label {
			t.Errorf("issueKinds[%q].label = %q, want %q", c.kind, ik.label, c.label)
		}
		if ik.prefix != c.prefix {
			t.Errorf("issueKinds[%q].prefix = %q, want %q", c.kind, ik.prefix, c.prefix)
		}
	}
}

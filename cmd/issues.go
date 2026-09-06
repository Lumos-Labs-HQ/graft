//go:build plugin_core || dev

package cmd

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/spf13/cobra"
)

const flashIssuesRepo = "Lumos-Labs-HQ/flash"

type issueKind struct {
	label  string
	prefix string
}

var issueKinds = map[string]issueKind{
	"bug":      {label: "bug", prefix: "[Bug]"},
	"feature":  {label: "enhancement", prefix: "[Feature]"},
	"question": {label: "question", prefix: "[Question]"},
	"docs":     {label: "documentation", prefix: "[Docs]"},
}

// junkTitles are low-effort titles that never make an actionable issue.
var junkTitles = map[string]bool{
	"test": true, "testing": true, "bug": true, "help": true, "asdf": true,
	"foo": true, "bar": true, "none": true, "na": true, "n/a": true,
	"issue": true, "error": true, "x": true, "todo": true, "fix": true,
	"it doesn't work": true, "doesnt work": true, "broken": true,
}

var issuesCmd = &cobra.Command{
	Use:   "issues",
	Short: "Report a bug or feature request to the FlashORM repo",
	Long: `
Compose a complete, well-structured issue and submit it to the FlashORM
repository (` + flashIssuesRepo + `).

Runtime diagnostics (Flash version, OS/arch, Go version, and the configured
database provider) are collected and attached automatically, so a report is
always reproducible.

The command validates the report before filing it — a real title, a substantive
description, and (for bugs) reproduction steps or expected/actual behavior — and
refuses to submit low-effort issues. This makes it safe for AI agents to file
bugs they encounter while using Flash.

Submission:
  - With the GitHub CLI (` + "`gh`" + `) installed and authenticated, the issue is
    created directly and its URL is printed.
  - Otherwise a pre-filled "New issue" URL is printed (and opened in a browser
    when running interactively).

Examples:
  # Preview the composed report without submitting (recommended first step):
  flash issues --print -k bug -t "gen: RETURNING dropped for SQLite inserts" \
    -b "flash gen produces an insert helper that never returns the row." \
    --repro "1. flash init --sqlite  2. add INSERT ... RETURNING :one  3. flash gen" \
    --expected "the function returns the inserted row" \
    --actual "the function returns void"

  # File it non-interactively (for agents):
  flash issues -y -k bug -t "..." -b "..." --repro "..."

  # Force the browser/URL path instead of the gh CLI:
  flash issues --web -k feature -t "..." -b "..."`,

	RunE: runIssues,
}

func runIssues(cmd *cobra.Command, args []string) error {
	title, _ := cmd.Flags().GetString("title")
	kind, _ := cmd.Flags().GetString("kind")
	body, _ := cmd.Flags().GetString("body")
	repro, _ := cmd.Flags().GetString("repro")
	expected, _ := cmd.Flags().GetString("expected")
	actual, _ := cmd.Flags().GetString("actual")
	extraLabels, _ := cmd.Flags().GetStringArray("label")
	web, _ := cmd.Flags().GetBool("web")
	printOnly, _ := cmd.Flags().GetBool("print")
	yes, _ := cmd.Flags().GetBool("yes")
	// --force is a persistent root flag; treat it as an alias for --yes.
	force, _ := cmd.Flags().GetBool("force")

	kind = strings.ToLower(strings.TrimSpace(kind))
	ik, ok := issueKinds[kind]
	if !ok {
		return fmt.Errorf("invalid --kind %q: must be one of bug, feature, question, docs", kind)
	}

	if err := validateIssueInput(kind, title, body, repro, expected, actual); err != nil {
		return err
	}

	title = prefixIssueTitle(ik.prefix, title)
	fullBody := composeIssueBody(kind, body, repro, expected, actual)
	labels := mergeIssueLabels(ik.label, extraLabels)

	// --print: compose and show, never submit.
	if printOnly {
		fmt.Printf("Title: %s\n", title)
		if len(labels) > 0 {
			fmt.Printf("Labels: %s\n", strings.Join(labels, ", "))
		}
		fmt.Printf("\n%s\n", fullBody)
		return nil
	}

	// Confirm unless explicitly skipped (agents pass -y; humans may pass -f).
	if !yes && !force {
		fmt.Printf("About to file this issue on %s:\n\n", flashIssuesRepo)
		fmt.Printf("Title: %s\n", title)
		if len(labels) > 0 {
			fmt.Printf("Labels: %s\n", strings.Join(labels, ", "))
		}
		fmt.Printf("\n%s\n\n", fullBody)
		if !confirmIssuePrompt("Create this issue? [y/N] ") {
			fmt.Println("Aborted. (Use --print to preview, or -y to skip this prompt.)")
			return nil
		}
	}

	// Prefer the gh CLI when available and not forced onto the web path.
	if !web {
		if ghPath, err := exec.LookPath("gh"); err == nil {
			ghArgs := []string{"issue", "create", "--repo", flashIssuesRepo, "--title", title, "--body", fullBody}
			for _, l := range labels {
				ghArgs = append(ghArgs, "--label", l)
			}
			out, err := exec.Command(ghPath, ghArgs...).CombinedOutput()
			if err == nil {
				fmt.Print(string(out))
				if !strings.HasSuffix(string(out), "\n") {
					fmt.Println()
				}
				fmt.Println("✅ Issue created.")
				return nil
			}
			fmt.Fprintf(os.Stderr, "gh issue create failed (%v); falling back to a browser link.\n", err)
			if s := strings.TrimSpace(string(out)); s != "" {
				fmt.Fprintln(os.Stderr, s)
			}
		}
	}

	// Fallback: a pre-filled "New issue" URL.
	issueURL := newIssueURL(title, fullBody, labels)
	fmt.Println("Open this URL to submit the issue:")
	fmt.Println(issueURL)
	if isInteractive() {
		if err := issueOpenURL(issueURL); err != nil {
			fmt.Fprintf(os.Stderr, "(could not open a browser automatically: %v)\n", err)
		}
	}
	return nil
}

// validateIssueInput enforces that a report is actually actionable. It returns a
// specific error naming the field to fix so an agent can correct and retry.
func validateIssueInput(kind, title, body, repro, expected, actual string) error {
	t := strings.TrimSpace(title)
	if t == "" {
		return fmt.Errorf("a title is required: pass -t/--title with a specific summary")
	}
	if len(t) < 10 {
		return fmt.Errorf("title is too short (%d chars): use -t/--title with a specific, descriptive summary (>= 10 chars)", len(t))
	}
	if junkTitles[strings.ToLower(t)] {
		return fmt.Errorf("title %q is not descriptive: summarize the actual problem, e.g. \"gen: RETURNING dropped for SQLite inserts\"", t)
	}

	b := strings.TrimSpace(body)
	if b == "" {
		return fmt.Errorf("a description is required: pass -b/--body explaining what happened")
	}
	if len(b) < 30 {
		return fmt.Errorf("description is too short (%d chars): pass -b/--body with enough detail to understand the report (>= 30 chars)", len(b))
	}

	if kind == "bug" {
		hasRepro := strings.TrimSpace(repro) != ""
		hasExpActual := strings.TrimSpace(expected) != "" && strings.TrimSpace(actual) != ""
		if !hasRepro && !hasExpActual {
			return fmt.Errorf("a bug report needs reproduction detail: pass --repro with steps, or both --expected and --actual")
		}
	}
	return nil
}

// prefixIssueTitle prepends the kind prefix unless the title already carries a
func prefixIssueTitle(prefix, title string) string {
	t := strings.TrimSpace(title)
	if strings.HasPrefix(t, "[") {
		return t
	}
	return prefix + " " + t
}

// issues.md / .github/ISSUE_TEMPLATE/bug_report.md.
func composeIssueBody(kind, body, repro, expected, actual string) string {
	var b strings.Builder

	b.WriteString("## Description\n\n")
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")

	if s := strings.TrimSpace(repro); s != "" {
		b.WriteString("\n## Steps to Reproduce\n\n")
		b.WriteString(s)
		b.WriteString("\n")
	}
	if s := strings.TrimSpace(expected); s != "" {
		b.WriteString("\n## Expected behavior\n\n")
		b.WriteString(s)
		b.WriteString("\n")
	}
	if s := strings.TrimSpace(actual); s != "" {
		b.WriteString("\n## Actual behavior\n\n")
		b.WriteString(s)
		b.WriteString("\n")
	}

	b.WriteString("\n## Environment\n\n")
	b.WriteString(issueEnvironmentTable())

	b.WriteString("\n---\n_Filed with `flash issues`._\n")
	return b.String()
}

// issueEnvironmentTable renders a Markdown table of runtime diagnostics. The DB
func issueEnvironmentTable() string {
	provider := "unknown (no flash.toml found)"
	cache := "unknown"
	if cfg, err := config.Load(); err == nil {
		if cfg.Database.Provider != "" {
			provider = cfg.Database.Provider
		}
		if cfg.Cache.Enabled {
			cache = "enabled"
		} else {
			cache = "disabled"
		}
	}

	rows := [][2]string{
		{"Flash version", Version},
		{"OS / arch", runtime.GOOS + "/" + runtime.GOARCH},
		{"Go runtime", runtime.Version()},
		{"DB provider", provider},
		{"Cache", cache},
	}

	var b strings.Builder
	b.WriteString("| | |\n|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s |\n", r[0], r[1])
	}
	return b.String()
}

// mergeIssueLabels combines the kind's label with any user-supplied labels,
func mergeIssueLabels(kindLabel string, extra []string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(l string) {
		l = strings.TrimSpace(l)
		if l == "" || seen[l] {
			return
		}
		seen[l] = true
		out = append(out, l)
	}
	add(kindLabel)
	for _, l := range extra {
		add(l)
	}
	return out
}

// newIssueURL builds a pre-filled GitHub "New issue" URL.
func newIssueURL(title, body string, labels []string) string {
	q := url.Values{}
	q.Set("title", title)
	q.Set("body", body)
	if len(labels) > 0 {
		q.Set("labels", strings.Join(labels, ","))
	}
	return "https://github.com/" + flashIssuesRepo + "/issues/new?" + q.Encode()
}

// confirmIssuePrompt prints prompt and returns true only on an explicit yes.
func confirmIssuePrompt(prompt string) bool {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// spawn a browser for a human, not an agent/CI.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func issueOpenURL(target string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "windows":
		name, args = "cmd", []string{"/c", "start", target}
	case "darwin":
		name, args = "open", []string{target}
	default:
		name, args = "xdg-open", []string{target}
	}
	return exec.Command(name, args...).Start()
}

func init() {
	issuesCmd.Flags().StringP("title", "t", "", "Issue title (a specific one-line summary)")
	issuesCmd.Flags().StringP("kind", "k", "bug", "Issue kind: bug, feature, question, docs")
	issuesCmd.Flags().StringP("body", "b", "", "Issue description (what happened)")
	issuesCmd.Flags().String("repro", "", "Steps to reproduce (required for bugs, unless --expected and --actual are given)")
	issuesCmd.Flags().String("expected", "", "Expected behavior")
	issuesCmd.Flags().String("actual", "", "Actual behavior (include exact error text)")
	issuesCmd.Flags().StringArrayP("label", "l", nil, "Additional label (repeatable)")
	issuesCmd.Flags().Bool("web", false, "Use a pre-filled browser URL instead of the gh CLI")
	issuesCmd.Flags().Bool("print", false, "Compose and print the report without submitting")
	issuesCmd.Flags().BoolP("yes", "y", false, "Submit without the confirmation prompt")
}

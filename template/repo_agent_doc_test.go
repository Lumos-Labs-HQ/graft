package template

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRootAgentGuide_Integrity verifies that AGENT.md exists in the repo root
// and documents all required architecture, packages, generators, invariants,
// and testing standards.
func TestRootAgentGuide_Integrity(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to get caller info")
	}

	repoRoot := filepath.Join(filepath.Dir(currentFile), "..")
	agentDocPath := filepath.Join(repoRoot, "AGENT.md")

	contentBytes, err := os.ReadFile(agentDocPath)
	if err != nil {
		t.Fatalf("AGENT.md not found at repo root (%s): %v", agentDocPath, err)
	}

	content := string(contentBytes)
	if len(strings.TrimSpace(content)) == 0 {
		t.Fatal("AGENT.md is empty")
	}

	// 1. Assert Title and Header
	if !strings.Contains(content, "# AGENT.md") {
		t.Errorf("AGENT.md missing main header '# AGENT.md'")
	}

	// 2. Assert All Mandatory Sections
	requiredSections := []string{
		"## 1. System Overview & Philosophy",
		"## 2. Repository Layout & Package Map",
		"## 3. End-to-End Compilation Pipeline",
		"## 4. Critical Invariants & Rules for AI Agents",
		"## 5. Testing Standards & Conventions",
		"## 6. Checklist for Changes",
	}
	for _, sec := range requiredSections {
		if !strings.Contains(content, sec) {
			t.Errorf("AGENT.md missing required section: %q", sec)
		}
	}

	// 3. Assert All 6 Language Generators are Documented
	generators := []string{
		"gogen",
		"jsgen",
		"pygen",
		"kotlingen",
		"javagen",
		"rustgen",
	}
	for _, gen := range generators {
		if !strings.Contains(content, gen) {
			t.Errorf("AGENT.md does not document generator: %q", gen)
		}
	}

	// 4. Assert Key Invariants and Rules are Covered
	invariants := []string{
		"Zero Runtime Reflection",
		"regex_cache",
		"ShouldRegenerateFileForOutput",
		"PurgeOrphanedOutputs",
		"sharedStructPlan",
		"task smoke",
	}
	for _, inv := range invariants {
		if !strings.Contains(content, inv) {
			t.Errorf("AGENT.md missing critical invariant: %q", inv)
		}
	}
}

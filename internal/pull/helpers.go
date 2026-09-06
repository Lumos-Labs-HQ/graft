package pull

import (
	"fmt"
	"regexp"
	"strings"
)

// Pre-compiled regexes used by the pull service.
var (
	whitespaceRegex = regexp.MustCompile(`\s+`)
	indexPattern    = regexp.MustCompile(`(?i)^\s*(CREATE\s+(?:UNIQUE\s+)?INDEX\s+[^;]+;)`)
)

// extractTableSQL extracts the CREATE TABLE statement for a specific table from content,
// only if the CREATE TABLE line is NOT commented out.
func (s *Service) extractTableSQL(content, tableName string) string {
	startPattern := fmt.Sprintf(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["'\x60]?%s["'\x60]?\s*\(`, regexp.QuoteMeta(tableName))
	startRe := regexp.MustCompile(startPattern)
	startMatch := startRe.FindStringIndex(content)
	if startMatch == nil {
		return ""
	}

	// Check if this CREATE TABLE is on a commented line
	lineStart := strings.LastIndex(content[:startMatch[0]], "\n") + 1
	linePrefix := strings.TrimSpace(content[lineStart:startMatch[0]])
	if strings.HasPrefix(linePrefix, "--") {
		return "" // commented out — don't touch
	}

	// Find the matching closing parenthesis
	start := startMatch[0]
	parenStart := startMatch[1] - 1
	depth := 0
	endPos := -1

	for i := parenStart; i < len(content); i++ {
		if content[i] == '(' {
			depth++
		} else if content[i] == ')' {
			depth--
			if depth == 0 {
				endPos = i + 1
				break
			}
		}
	}

	if endPos == -1 {
		return ""
	}

	// Find the semicolon after the closing paren
	semiPos := strings.Index(content[endPos:], ";")
	if semiPos != -1 {
		endPos = endPos + semiPos + 1
	}

	var tableSQL strings.Builder
	tableSQL.WriteString(content[start:endPos])

	// Also capture any CREATE INDEX statements that follow
	remaining := content[endPos:]
	for {
		match := indexPattern.FindStringSubmatch(remaining)
		if match == nil {
			break
		}
		tableSQL.WriteString("\n" + strings.TrimSpace(match[1]))
		remaining = remaining[len(match[0]):]
	}

	return strings.TrimSpace(tableSQL.String())
}

// replaceTableInContent replaces only the CREATE TABLE...); block in content,
// preserving all surrounding comments and whitespace.
// If the CREATE TABLE is on a commented line, returns content unchanged.
func (s *Service) replaceTableInContent(content, tableName, newTableSQL string) string {
	startPattern := fmt.Sprintf(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["'\x60]?%s["'\x60]?\s*\(`, regexp.QuoteMeta(tableName))
	startRe := regexp.MustCompile(startPattern)
	startMatch := startRe.FindStringIndex(content)
	if startMatch == nil {
		return content
	}

	// Never touch commented-out CREATE TABLE
	lineStart := strings.LastIndex(content[:startMatch[0]], "\n") + 1
	linePrefix := strings.TrimSpace(content[lineStart:startMatch[0]])
	if strings.HasPrefix(linePrefix, "--") {
		return content
	}

	// Find matching closing paren + semicolon
	parenStart := startMatch[1] - 1
	depth := 0
	endPos := -1
	for i := parenStart; i < len(content); i++ {
		if content[i] == '(' {
			depth++
		} else if content[i] == ')' {
			depth--
			if depth == 0 {
				endPos = i + 1
				break
			}
		}
	}
	if endPos == -1 {
		return content
	}
	// Include trailing semicolon if present
	rest := content[endPos:]
	if idx := strings.Index(rest, ";"); idx != -1 && strings.TrimSpace(rest[:idx]) == "" {
		endPos = endPos + idx + 1
	}

	return cleanSchemaContent(content[:startMatch[0]] + newTableSQL + content[endPos:])
}

// compareTableSQL compares two table SQL definitions (ignoring whitespace differences)
func (s *Service) compareTableSQL(sql1, sql2 string) bool {
	normalize := func(sql string) string {
		sql = strings.ToLower(sql)
		sql = whitespaceRegex.ReplaceAllString(sql, " ")
		sql = strings.TrimSpace(sql)
		return sql
	}
	return normalize(sql1) == normalize(sql2)
}

// isFileCommentedOut checks if a file is already fully commented out
func (s *Service) isFileCommentedOut(content string) bool {
	lines := strings.SplitSeq(content, "\n")
	for line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "--") {
			return false
		}
	}
	return true
}

func (s *Service) commentOutFile(content, tableName string) string {
	var sb strings.Builder

	sb.WriteString("-- ============================================================\n")
	sb.WriteString(fmt.Sprintf("-- TABLE DROPPED: '%s' no longer exists in database\n", tableName))
	sb.WriteString("-- This file has been commented out by 'flash pull'\n")
	sb.WriteString("-- You can delete this file or uncomment to recreate the table\n")
	sb.WriteString("-- ============================================================\n\n")

	lines := strings.SplitSeq(content, "\n")
	for line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			sb.WriteString("\n")
		} else if strings.HasPrefix(trimmed, "--") {
			sb.WriteString(line + "\n")
		} else {
			sb.WriteString("-- " + line + "\n")
		}
	}

	return sb.String()
}

// cleanSchemaContent removes any non-schema SQL statements (INSERT, UPDATE, DELETE, etc.)
// from file content, keeping only CREATE/DROP/ALTER/index statements and comments.
func cleanSchemaContent(content string) string {
	// Split by semicolons to get individual statements, preserving structure
	var out strings.Builder
	lines := strings.Split(content, "\n")
	skipUntilSemi := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Always keep blank lines and comment lines
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			if !skipUntilSemi {
				out.WriteString(line + "\n")
			}
			continue
		}

		upper := strings.ToUpper(trimmed)

		if skipUntilSemi {
			// Consume until semicolon ends the statement
			if strings.Contains(trimmed, ";") {
				skipUntilSemi = false
			}
			continue
		}

		// Check if this line starts a non-schema statement
		if strings.HasPrefix(upper, "INSERT") ||
			strings.HasPrefix(upper, "UPDATE") ||
			strings.HasPrefix(upper, "DELETE") ||
			strings.HasPrefix(upper, "TRUNCATE") ||
			strings.HasPrefix(upper, "COPY") ||
			strings.HasPrefix(upper, "CALL") ||
			strings.HasPrefix(upper, "DO ") ||
			strings.HasPrefix(upper, "SELECT") {
			// Skip this line; if no semicolon yet, skip until we find one
			if !strings.Contains(trimmed, ";") {
				skipUntilSemi = true
			}
			continue
		}

		out.WriteString(line + "\n")
	}

	// Trim trailing blank lines
	result := strings.TrimRight(out.String(), "\n")
	if result != "" {
		result += "\n"
	}
	return result
}

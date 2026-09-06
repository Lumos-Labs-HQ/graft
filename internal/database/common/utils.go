package common

import (
	"strings"
)

type QueryResult struct {
	Columns []string
	Rows    []map[string]any
}

// ParseSQLStatements splits SQL migration text into individual statements,
// blocks, and SQLite BEGIN...END trigger blocks so that semicolons inside
func ParseSQLStatements(sql string) []string {
	var statements []string
	var current strings.Builder

	const (
		normal = iota
		singleQuote
		doubleQuote
		backtick
		lineComment
		blockComment
		dollarQuote
	)
	state := normal
	var dollarTag string
	beginDepth := 0 // tracks SQLite BEGIN...END blocks (triggers)

	for i := 0; i < len(sql); {
		ch := sql[i]

		switch state {
		case lineComment:
			if ch == '\n' {
				state = normal
			}
			i++
			continue

		case blockComment:
			if ch == '*' && i+1 < len(sql) && sql[i+1] == '/' {
				state = normal
				i += 2
				continue
			}
			i++
			continue

		case singleQuote:
			if ch == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					current.WriteString("''")
					i += 2
					continue
				}
				state = normal
			}
			current.WriteByte(ch)
			i++
			continue

		case doubleQuote:
			if ch == '"' {
				if i+1 < len(sql) && sql[i+1] == '"' {
					current.WriteString("\"\"")
					i += 2
					continue
				}
				state = normal
			}
			current.WriteByte(ch)
			i++
			continue

		case backtick:
			if ch == '`' {
				if i+1 < len(sql) && sql[i+1] == '`' {
					current.WriteString("``")
					i += 2
					continue
				}
				state = normal
			}
			current.WriteByte(ch)
			i++
			continue

		case dollarQuote:
			if ch == '$' {
				// Try to find a matching closing tag
				tagEnd := i + 1
				for tagEnd < len(sql) && sql[tagEnd] != '$' {
					if !isDollarTagChar(sql[tagEnd]) {
						break
					}
					tagEnd++
				}
				if tagEnd < len(sql) && sql[tagEnd] == '$' {
					tag := sql[i : tagEnd+1]
					if tag == dollarTag {
						state = normal
						current.WriteString(tag)
						i = tagEnd + 1
						continue
					}
				}
			}
			current.WriteByte(ch)
			i++
			continue
		}

		// state == normal
		if ch == '-' && i+1 < len(sql) && sql[i+1] == '-' {
			state = lineComment
			i += 2
			continue
		}

		if ch == '/' && i+1 < len(sql) && sql[i+1] == '*' {
			state = blockComment
			i += 2
			continue
		}

		if ch == '\'' {
			state = singleQuote
			current.WriteByte(ch)
			i++
			continue
		}

		if ch == '"' {
			state = doubleQuote
			current.WriteByte(ch)
			i++
			continue
		}

		if ch == '`' {
			state = backtick
			current.WriteByte(ch)
			i++
			continue
		}

		if ch == '$' {
			tagEnd := i + 1
			for tagEnd < len(sql) && isDollarTagChar(sql[tagEnd]) {
				tagEnd++
			}
			if tagEnd < len(sql) && sql[tagEnd] == '$' {
				dollarTag = sql[i : tagEnd+1]
				state = dollarQuote
				current.WriteString(dollarTag)
				i = tagEnd + 1
				continue
			}
		}

		// Track SQLite BEGIN...END blocks (triggers).
		// END followed by ; closes the block.
		if (ch == 'B' || ch == 'b') && i+5 <= len(sql) {
			word := sql[i : i+5]
			if strings.EqualFold(word, "BEGIN") && (i+5 >= len(sql) || !isIdentChar(sql[i+5])) &&
				(i == 0 || !isIdentChar(sql[i-1])) {
				// Check that this is not BEGIN TRANSACTION/DEFERRED/IMMEDIATE/EXCLUSIVE
				rest := strings.TrimSpace(sql[i+5:])
				isTransaction := strings.HasPrefix(strings.ToUpper(rest), "TRANSACTION") ||
					strings.HasPrefix(strings.ToUpper(rest), "DEFERRED") ||
					strings.HasPrefix(strings.ToUpper(rest), "IMMEDIATE") ||
					strings.HasPrefix(strings.ToUpper(rest), "EXCLUSIVE")
				if !isTransaction {
					beginDepth++
					current.WriteString(sql[i : i+5])
					i += 5
					continue
				}
			}
		}
		if beginDepth > 0 && (ch == 'E' || ch == 'e') && i+3 <= len(sql) {
			word := sql[i : i+3]
			if strings.EqualFold(word, "END") && (i+3 >= len(sql) || !isIdentChar(sql[i+3])) &&
				(i == 0 || !isIdentChar(sql[i-1])) {
				beginDepth--
				current.WriteString(sql[i : i+3])
				i += 3
				continue
			}
		}

		if ch == ';' {
			if beginDepth > 0 {
				current.WriteByte(ch)
				i++
				continue
			}
			stmt := strings.TrimSpace(current.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			current.Reset()
			i++
			continue
		}

		current.WriteByte(ch)
		i++
	}

	if current.Len() > 0 {
		stmt := strings.TrimSpace(current.String())
		if stmt != "" {
			statements = append(statements, stmt)
		}
	}

	return statements
}

func isIdentChar(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_'
}

func isDollarTagChar(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_'
}

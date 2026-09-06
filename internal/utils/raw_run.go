package utils

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/database"
)

func RunRaw(cmd *cobra.Command, args []string, queryFlag bool, fileFlag bool) error {
	input := args[0]
	var sqlContent string
	var isFile bool

	if queryFlag {
		sqlContent = input
		isFile = false
	} else if fileFlag {
		if _, err := os.Stat(input); os.IsNotExist(err) {
			return fmt.Errorf("SQL file not found: %s", input)
		}
		content, err := os.ReadFile(input)
		if err != nil {
			return fmt.Errorf("failed to read SQL file: %w", err)
		}
		sqlContent = string(content)
		isFile = true
	} else {
		if _, err := os.Stat(input); err == nil {
			content, err := os.ReadFile(input)
			if err != nil {
				return fmt.Errorf("failed to read SQL file: %w", err)
			}
			sqlContent = string(content)
			isFile = true
		} else {
			sqlContent = input
			isFile = false
		}
	}

	if len(sqlContent) == 0 {
		if isFile {
			return fmt.Errorf("SQL file is empty: %s", input)
		}
		return fmt.Errorf("SQL query is empty")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	adapter := database.NewAdapter(cfg.Database.Provider)

	dbURL, err := cfg.GetDatabaseURL()
	if err != nil {
		return fmt.Errorf("failed to get database URL: %w", err)
	}

	ctx := context.Background()
	if err := adapter.Connect(ctx, dbURL); err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer adapter.Close()

	if isFile {
		fmt.Printf("📄 Executing SQL file: %s\n", input)
	} else {
		fmt.Printf("📄 Executing SQL query\n")
	}
	fmt.Printf("🎯 Database: %s\n", cfg.Database.Provider)
	fmt.Println()

	query := strings.TrimSpace(string(sqlContent))

	queryUpper := strings.ToUpper(query)
	isSelectQuery := strings.HasPrefix(queryUpper, "SELECT") ||
		strings.HasPrefix(queryUpper, "SHOW") ||
		strings.HasPrefix(queryUpper, "DESCRIBE") ||
		strings.HasPrefix(queryUpper, "EXPLAIN") ||
		strings.HasPrefix(queryUpper, "WITH")

	if isSelectQuery {
		fmt.Println("⚡ Executing query...")
		result, err := adapter.ExecuteQuery(ctx, query)
		if err != nil {
			return fmt.Errorf("failed to execute query: %w", err)
		}

		if len(result.Rows) == 0 {
			fmt.Println("✅ Query executed successfully")
			fmt.Println("📊 No rows returned")
			return nil
		}

		fmt.Printf("✅ Query executed successfully\n")
		fmt.Printf("📊 %d row(s) returned\n\n", len(result.Rows))

		displayResultsTable(result.Columns, result.Rows)
	} else {
		statements := splitSQLStatements(query)

		if len(statements) == 0 {
			return fmt.Errorf("no SQL statements found in file")
		}

		fmt.Printf("📝 Found %d SQL statement(s)\n", len(statements))
		fmt.Println()

		for i, statement := range statements {
			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}

			// PostgreSQL does not support ADD CONSTRAINT IF NOT EXISTS.
			// Rewrite to a DO block that checks pg_constraint first.
			if cfg.Database.Provider == "postgresql" || cfg.Database.Provider == "postgres" {
				statement = rewriteAddConstraintIfNotExists(statement)
			}

			fmt.Printf("⚡ Executing statement %d...\n", i+1)

			if err := adapter.ExecuteMigration(ctx, statement); err != nil {
				return fmt.Errorf("failed to execute statement %d: %w", i+1, err)
			}

			fmt.Printf("✅ Statement %d executed successfully\n", i+1)
		}

		fmt.Println()
		fmt.Printf("🎉 All statements executed successfully!\n")
	}

	return nil
}

func displayResultsTable(columns []string, rows []map[string]any) {
	if len(rows) == 0 {
		return
	}

	colWidths := make(map[string]int)
	for _, col := range columns {
		colWidths[col] = len(col)
	}

	for _, row := range rows {
		for _, col := range columns {
			val := formatValue(row[col])
			if len(val) > colWidths[col] {
				colWidths[col] = len(val)
			}
		}
	}

	fmt.Print("┌")
	for i, col := range columns {
		fmt.Print(strings.Repeat("─", colWidths[col]+2))
		if i < len(columns)-1 {
			fmt.Print("┬")
		}
	}
	fmt.Println("┐")

	fmt.Print("│")
	for _, col := range columns {
		fmt.Printf(" %-*s │", colWidths[col], col)
	}
	fmt.Println()

	fmt.Print("├")
	for i, col := range columns {
		fmt.Print(strings.Repeat("─", colWidths[col]+2))
		if i < len(columns)-1 {
			fmt.Print("┼")
		}
	}
	fmt.Println("┤")

	for _, row := range rows {
		fmt.Print("│")
		for _, col := range columns {
			val := formatValue(row[col])
			fmt.Printf(" %-*s │", colWidths[col], val)
		}
		fmt.Println()
	}

	fmt.Print("└")
	for i, col := range columns {
		fmt.Print(strings.Repeat("─", colWidths[col]+2))
		if i < len(columns)-1 {
			fmt.Print("┴")
		}
	}
	fmt.Println("┘")
}

func formatValue(val any) string {
	if val == nil {
		return "NULL"
	}
	return fmt.Sprintf("%v", val)
}

func splitSQLStatements(content string) []string {
	var statements []string

	parts := strings.SplitSeq(content, ";")

	for part := range parts {
		// Strip comment lines, then check if anything remains
		var nonCommentLines []string
		for line := range strings.SplitSeq(part, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "--") {
				nonCommentLines = append(nonCommentLines, line)
			}
		}
		statement := strings.TrimSpace(strings.Join(nonCommentLines, "\n"))
		if statement != "" {
			statements = append(statements, statement)
		}
	}

	return statements
}

// rewriteAddConstraintIfNotExists rewrites:
//
//	ALTER TABLE t ADD CONSTRAINT IF NOT EXISTS name ...
//
// to a DO block that skips if the constraint already exists,
// because PostgreSQL does not support IF NOT EXISTS for ADD CONSTRAINT.
func rewriteAddConstraintIfNotExists(stmt string) string {
	re := regexp.MustCompile(`(?i)ALTER\s+TABLE\s+(\S+)\s+ADD\s+CONSTRAINT\s+IF\s+NOT\s+EXISTS\s+(\S+)\s+(.+)`)
	m := re.FindStringSubmatch(stmt)
	if m == nil {
		return stmt
	}
	table, name, rest := m[1], m[2], strings.TrimRight(strings.TrimSpace(m[3]), ";")
	return fmt.Sprintf(
		`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = '%s') THEN ALTER TABLE %s ADD CONSTRAINT %s %s; END IF; END $$`,
		name, table, name, rest,
	)
}

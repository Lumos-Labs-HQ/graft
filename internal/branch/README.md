# Branch Feature Implementation

## Progress

### ✅ Phase 1: Core Branch Manager (COMPLETED)

**Files Created:**
- `metadata.go` - Branch metadata storage and management
- `manager.go` - Core branch operations (create, switch, list)
- `diff.go` - Schema comparison between branches

**Features Implemented:**
- ✅ Branch metadata structure
- ✅ Create branch (clone schema + data)
- ✅ Switch between branches
- ✅ List all branches
- ✅ Get current branch
- ✅ Schema diff between branches
- ✅ PostgreSQL schema cloning
- ✅ MySQL database cloning
- ✅ Metadata persistence in `.flash/branches.json`

**Key Components:**

1. **BranchMetadata**: Stores branch info (name, parent, schema, created_at)
2. **BranchStore**: Manages all branches and current branch
3. **Manager**: Core operations for branch management
4. **SchemaDiff**: Compares schemas between branches

---

## Next Steps

### ✅ Phase 2: Database Adapter Extensions (COMPLETED)

**Files Created/Modified:**
- ✅ `internal/database/adapter.go` - Added 6 branch methods to interface
- ✅ `internal/database/postgres/branch.go` - PostgreSQL schema-based branching
- ✅ `internal/database/mysql/branch.go` - MySQL database-based branching
- ✅ `internal/database/sqlite/branch.go` - SQLite stub (file-based branching)

**Methods Implemented:**
```go
CreateBranchSchema(ctx, branchName) error
DropBranchSchema(ctx, branchName) error
CloneSchemaToBranch(ctx, sourceSchema, targetSchema) error
GetSchemaForBranch(ctx, branchSchema) ([]SchemaTable, error)
SetActiveSchema(ctx, schemaName) error
GetTableNamesInSchema(ctx, schemaName) ([]string, error)
```

**Features:**
- ✅ PostgreSQL: CREATE SCHEMA + clone tables with data
- ✅ MySQL: CREATE DATABASE + clone tables with data
- ✅ SQLite: Placeholder (requires file-based approach)
- ✅ Schema introspection per branch
- ✅ Active schema switching

---

### ✅ Phase 3: CLI Commands (COMPLETED)

**File Created:**
- ✅ `cmd/branch.go` - Complete CLI interface for branch operations

**Commands Implemented:**
```bash
flash branch create <name>           # Create new branch with confirmation
flash branch switch <name>           # Switch to existing branch
flash branch list                    # List all branches with status
flash branch status                  # Show current active branch
flash branch diff <branch1> <branch2> # Compare schema between branches
```

**Features:**
- ✅ Interactive confirmation prompts (can skip with --force)
- ✅ Colored output for better UX
- ✅ Human-readable timestamps (e.g., "5 mins ago")
- ✅ Active branch indicator in list
- ✅ Schema diff visualization

---

### 🔄 Phase 4: Branch-Aware Migrations (TODO)

**Files to Modify:**
- `internal/migrator/migrator.go` - Make migrations branch-aware
- `internal/migrator/operations.go` - Track migrations per branch

**Changes Needed:**
1. Add `branch` column to `flash_migrations` table
2. Filter migrations by current branch
3. Apply migrations only to active branch schema

---

### 🔄 Phase 4: CLI Commands (TODO)

**File to Create:**
- `cmd/branch.go` - Branch CLI commands

**Commands to Implement:**
```bash
flash branch create <name>           # Create new branch
flash branch switch <name>           # Switch to branch
flash branch list                    # List all branches
flash branch status                  # Show current branch
flash branch diff <branch1> <branch2> # Compare branches
```

---

### 🔄 Phase 5: Config Integration (TODO)

**Files to Modify:**
- `internal/config/config.go` - Add branch awareness

**Changes:**
- Track current branch in config
- Provide branch-specific database URLs

---

## Usage Example (After Full Implementation)

```bash
# Check current branch
$ flash branch status
Current branch: main

# Create dev branch
$ flash branch create dev
⚠️  This will copy all schema and data from 'main' to 'dev'. Continue? (y/N): y
✅ Branch 'dev' created successfully
✅ Copied 5 tables with 1,234 rows

# Switch to dev
$ flash branch switch dev
✅ Switched to branch 'dev'

# Work on dev branch
$ flash migrate "add users table"
$ flash apply

# Compare with main
$ flash branch diff main dev
Tables added:
  + users (3 columns)

# List branches
$ flash branch list
* dev    (active) - Created 5 mins ago
  main   (default) - Created 2 days ago
```

---

## Technical Details

### Metadata Storage
Location: `db/migrations/.flash/branches.json`

```json
{
  "current": "main",
  "branches": [
    {
      "name": "main",
      "parent": "",
      "schema": "public",
      "created_at": "2025-11-14T10:00:00Z",
      "is_default": true
    },
    {
      "name": "dev",
      "parent": "main",
      "schema": "flash_branch_dev",
      "created_at": "2025-11-14T11:00:00Z",
      "is_default": false
    }
  ]
}
```

### Schema Naming Convention

- **PostgreSQL**: `flash_branch_<name>` (schema within same database)
- **MySQL**: `flash_branch_<name>` (separate database)
- **SQLite**: `flash_branch_<name>.db` (separate file)

### Migration Tracking

Each branch maintains independent migration history:
```sql
CREATE TABLE flash_migrations (
  id SERIAL PRIMARY KEY,
  migration_id VARCHAR(255) NOT NULL,
  name VARCHAR(255) NOT NULL,
  checksum VARCHAR(64),
  applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  branch VARCHAR(255) DEFAULT 'main'  -- NEW COLUMN
);
```

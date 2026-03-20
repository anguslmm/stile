# Task 5.2: CLI Caller Management

**Status:** not started
**Depends on:** Task 5.1 (role-based access control), Task 6 (auth — CallerStore, SQLiteStore)
**Needed by:** nothing (quality-of-life, replaces ad-hoc seed script)

---

## Goal

Add subcommands to the `stile` binary for managing callers and API keys from the command line. This replaces the throwaway `cmd/seed` script with proper CLI tooling that an operator can use in production.

Under the role-based model (Task 5.1), callers are just named identities. Tool access and credentials are determined by the role assigned to each API key, which is defined in the YAML config.

---

## 1. Subcommands

Use Go's `flag` package with subcommand dispatch (no external CLI framework).

### `stile add-caller`

```bash
stile add-caller --name angus
```

- `--name` (required): unique caller name
- `--db` (optional): path to SQLite database (default: from config, or `stile.db`)

Callers are named identities — access is determined by the roles on their API keys.

Prints confirmation on success. Returns non-zero exit code on error (duplicate name, etc).

### `stile add-key`

```bash
stile add-key --caller angus --role web-tools --label "angus laptop"
```

- `--caller` (required): name of existing caller
- `--role` (required): role this key grants — must match a role defined in config
- `--label` (optional): human-readable label for the key
- `--db` (optional): path to SQLite database
- `--config` (optional): path to config file (used to validate role exists)

Generates a cryptographically random API key with `sk-` prefix, hashes it with SHA-256, stores the hash, and prints the raw key **once**:

```
API key for angus (role: web-tools):
  sk-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4

Store this key securely — it cannot be retrieved again.
```

If `--config` is provided, validates that the role exists in config. Warns (but still creates) if not provided — the key is usable but the caller won't have any tool access until the role is defined in config.

### `stile list-callers`

```bash
stile list-callers
```

Lists all callers with their key count and roles. No secrets shown.

```
NAME     KEYS  ROLES
angus    2     web-tools, database
bob      1     full-access
```

### `stile remove-caller`

```bash
stile remove-caller --name angus
```

Deletes the caller and all their API keys (CASCADE). Prints confirmation. Requires `--force` if the caller has active keys (safety check).

### `stile revoke-key`

```bash
stile revoke-key --caller angus --label "angus laptop"
```

Revokes a specific key by label. If no label given, lists all keys for the caller (showing label, role, created-at — never the key itself) and asks which to revoke.

---

## 2. Subcommand Dispatch

Modify `cmd/gateway/main.go` to check `os.Args` for subcommands before entering the normal gateway flow:

```go
func main() {
    if len(os.Args) > 1 {
        switch os.Args[1] {
        case "add-caller":
            runAddCaller(os.Args[2:])
            return
        case "add-key":
            runAddKey(os.Args[2:])
            return
        case "list-callers":
            runListCallers(os.Args[2:])
            return
        case "remove-caller":
            runRemoveCaller(os.Args[2:])
            return
        case "revoke-key":
            runRevokeKey(os.Args[2:])
            return
        }
    }
    // ... normal gateway startup
}
```

Each `run*` function creates its own `flag.FlagSet`, opens the database, performs the operation, and exits.

---

## 3. Database Path Resolution

Subcommands need to find the database without necessarily loading the full config. Resolution order:

1. `--db` flag (explicit)
2. `--config` flag → load config → `server.db_path`
3. Default: `stile.db` in current directory

---

## 4. Cleanup

Once this task is complete, remove `cmd/seed/` — it's replaced by the real CLI.

---

## 5. Testable Deliverables

1. **add-caller creates caller:** run subcommand → caller exists in database
2. **add-caller duplicate name:** → non-zero exit, error message
3. **add-key generates valid key:** run subcommand → key hash in database, printed key starts with `sk-`, hashing printed key matches stored hash
4. **add-key unknown caller:** → non-zero exit, error message
5. **add-key validates role against config:** with `--config`, warns if role not in config
6. **list-callers shows all:** create 2 callers with keys → output lists both with correct info and roles
7. **remove-caller deletes:** create caller with key → remove → caller and keys gone
8. **revoke-key removes key:** create caller with 2 keys → revoke one → one remains

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```

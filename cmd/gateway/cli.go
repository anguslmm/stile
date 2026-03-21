package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/config"
)

func openStore(fs *flag.FlagSet, dbFlag, configFlag *string) (*auth.SQLiteStore, error) {
	// 1. Explicit --db flag.
	if *dbFlag != "" {
		return auth.NewSQLiteStore(*dbFlag)
	}
	// 2. --config flag → load config → server.db_path.
	if *configFlag != "" {
		cfg, err := config.Load(*configFlag)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		if p := cfg.Server().DBPath(); p != "" {
			return auth.NewSQLiteStore(p)
		}
	}
	// 3. Default.
	return auth.NewSQLiteStore("stile.db")
}

func runAddCaller(args []string) {
	fs := flag.NewFlagSet("add-caller", flag.ExitOnError)
	name := fs.String("name", "", "unique caller name (required)")
	db := fs.String("db", "", "path to SQLite database")
	cfg := fs.String("config", "", "path to config file")
	fs.Parse(args)

	if *name == "" {
		fmt.Fprintln(os.Stderr, "error: --name is required")
		os.Exit(1)
	}

	store, err := openStore(fs, db, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.AddCaller(*name); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Caller %q created.\n", *name)
}

func runAddKey(args []string) {
	fs := flag.NewFlagSet("add-key", flag.ExitOnError)
	caller := fs.String("caller", "", "name of existing caller (required)")
	label := fs.String("label", "", "human-readable label for the key")
	db := fs.String("db", "", "path to SQLite database")
	cfg := fs.String("config", "", "path to config file")
	fs.Parse(args)

	if *caller == "" {
		fmt.Fprintln(os.Stderr, "error: --caller is required")
		os.Exit(1)
	}

	store, err := openStore(fs, db, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	rawKey := generateAPIKey()
	hash := sha256.Sum256([]byte(rawKey))
	if err := store.AddKey(*caller, hash, *label); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("API key for %s:\n", *caller)
	fmt.Printf("  %s\n\n", rawKey)
	fmt.Println("Store this key securely — it cannot be retrieved again.")
}

func runAssignRole(args []string) {
	fs := flag.NewFlagSet("assign-role", flag.ExitOnError)
	caller := fs.String("caller", "", "name of existing caller (required)")
	role := fs.String("role", "", "role to assign (required)")
	db := fs.String("db", "", "path to SQLite database")
	cfgPath := fs.String("config", "", "path to config file (validates role exists)")
	fs.Parse(args)

	if *caller == "" || *role == "" {
		fmt.Fprintln(os.Stderr, "error: --caller and --role are required")
		os.Exit(1)
	}

	// If --config provided, validate role exists in config.
	if *cfgPath != "" {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: load config: %v\n", err)
			os.Exit(1)
		}
		found := false
		for _, r := range cfg.Roles() {
			if r.Name() == *role {
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "warning: role %q not found in config\n", *role)
		}
	}

	store, err := openStore(fs, db, cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.AssignRole(*caller, *role); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Role %q assigned to %q.\n", *role, *caller)
}

func runUnassignRole(args []string) {
	fs := flag.NewFlagSet("unassign-role", flag.ExitOnError)
	caller := fs.String("caller", "", "name of existing caller (required)")
	role := fs.String("role", "", "role to unassign (required)")
	db := fs.String("db", "", "path to SQLite database")
	cfg := fs.String("config", "", "path to config file")
	fs.Parse(args)

	if *caller == "" || *role == "" {
		fmt.Fprintln(os.Stderr, "error: --caller and --role are required")
		os.Exit(1)
	}

	store, err := openStore(fs, db, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.UnassignRole(*caller, *role); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Role %q unassigned from %q.\n", *role, *caller)
}

func runListCallers(args []string) {
	fs := flag.NewFlagSet("list-callers", flag.ExitOnError)
	db := fs.String("db", "", "path to SQLite database")
	cfg := fs.String("config", "", "path to config file")
	fs.Parse(args)

	store, err := openStore(fs, db, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	callers, err := store.ListCallers()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(callers) == 0 {
		fmt.Println("No callers found.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKEYS\tROLES")
	for _, c := range callers {
		roles := strings.Join(c.Roles, ", ")
		fmt.Fprintf(w, "%s\t%d\t%s\n", c.Name, c.KeyCount, roles)
	}
	w.Flush()
}

func runRemoveCaller(args []string) {
	fs := flag.NewFlagSet("remove-caller", flag.ExitOnError)
	name := fs.String("name", "", "caller name to remove (required)")
	force := fs.Bool("force", false, "force removal even if caller has active keys")
	db := fs.String("db", "", "path to SQLite database")
	cfg := fs.String("config", "", "path to config file")
	fs.Parse(args)

	if *name == "" {
		fmt.Fprintln(os.Stderr, "error: --name is required")
		os.Exit(1)
	}

	store, err := openStore(fs, db, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	if !*force {
		count, err := store.KeyCountForCaller(*name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if count > 0 {
			fmt.Fprintf(os.Stderr, "error: caller %q has %d active key(s); use --force to remove\n", *name, count)
			os.Exit(1)
		}
	}

	if err := store.DeleteCaller(*name); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Caller %q removed.\n", *name)
}

func runRevokeKey(args []string) {
	fs := flag.NewFlagSet("revoke-key", flag.ExitOnError)
	caller := fs.String("caller", "", "caller who owns the key (required)")
	label := fs.String("label", "", "label of the key to revoke")
	db := fs.String("db", "", "path to SQLite database")
	cfg := fs.String("config", "", "path to config file")
	fs.Parse(args)

	if *caller == "" {
		fmt.Fprintln(os.Stderr, "error: --caller is required")
		os.Exit(1)
	}

	store, err := openStore(fs, db, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// If no label given, list keys for the caller.
	if *label == "" {
		keys, err := store.ListKeys(*caller)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if len(keys) == 0 {
			fmt.Fprintf(os.Stderr, "error: caller %q has no keys\n", *caller)
			os.Exit(1)
		}
		fmt.Printf("Keys for %q (use --label to revoke):\n", *caller)
		for _, k := range keys {
			fmt.Printf("  label=%q  created=%s\n", k.Label, k.CreatedAt.Format("2006-01-02 15:04:05"))
		}
		os.Exit(0)
	}

	if err := store.RevokeKey(*caller, *label); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Key %q revoked for caller %q.\n", *label, *caller)
}

func generateAPIKey() string {
	return auth.GenerateAPIKey()
}

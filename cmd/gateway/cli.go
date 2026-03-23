package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/anguslmm/stile/internal/admin"
	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/config"
)

// cliAdmin defines the operations that CLI commands need.
// Both localAdmin (direct store access) and admin.Client (remote HTTP)
// satisfy this interface.
type cliAdmin interface {
	AddCaller(name string) error
	ListCallers() ([]auth.CallerInfo, error)
	DeleteCaller(name string) error
	KeyCountForCaller(name string) (int, error)
	CreateKey(callerName, label string) (string, error)
	ListKeys(callerName string) ([]auth.KeyInfo, error)
	RevokeKey(callerName, label string) error
	AssignRole(callerName, role string) error
	UnassignRole(callerName, role string) error
	Close() error
}

// Compile-time interface checks.
var (
	_ cliAdmin = (*localAdmin)(nil)
	_ cliAdmin = (*admin.Client)(nil)
)

// localAdmin wraps auth.Store for local database access.
type localAdmin struct {
	store auth.Store
}

func (a *localAdmin) AddCaller(name string) error          { return a.store.AddCaller(name) }
func (a *localAdmin) ListCallers() ([]auth.CallerInfo, error) { return a.store.ListCallers() }
func (a *localAdmin) DeleteCaller(name string) error        { return a.store.DeleteCaller(name) }
func (a *localAdmin) KeyCountForCaller(name string) (int, error) {
	return a.store.KeyCountForCaller(name)
}
func (a *localAdmin) CreateKey(callerName, label string) (string, error) {
	rawKey, err := auth.GenerateAPIKey()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(rawKey))
	if err := a.store.AddKey(callerName, hash, label); err != nil {
		return "", err
	}
	return rawKey, nil
}
func (a *localAdmin) ListKeys(callerName string) ([]auth.KeyInfo, error) {
	return a.store.ListKeys(callerName)
}
func (a *localAdmin) RevokeKey(callerName, label string) error {
	return a.store.RevokeKey(callerName, label)
}
func (a *localAdmin) AssignRole(callerName, role string) error {
	return a.store.AssignRole(callerName, role)
}
func (a *localAdmin) UnassignRole(callerName, role string) error {
	return a.store.UnassignRole(callerName, role)
}
func (a *localAdmin) Close() error { return a.store.Close() }

// cliFlags holds the shared flags for all CLI subcommands.
type cliFlags struct {
	db       *string
	driver   *string
	config   *string
	remote   *string
	adminKey *string
}

func addCLIFlags(fs *flag.FlagSet) *cliFlags {
	return &cliFlags{
		db:       fs.String("db", "", "database DSN (file path for sqlite, connection string for postgres)"),
		driver:   fs.String("driver", "", "database driver: sqlite (default) or postgres"),
		config:   fs.String("config", "", "path to config file"),
		remote:   fs.String("remote", "", "base URL of remote Stile admin API"),
		adminKey: fs.String("admin-key", "", "admin API key (or set STILE_ADMIN_KEY)"),
	}
}

// openAdmin returns a cliAdmin backed by either a remote HTTP client
// or a local database, depending on the flags.
func openAdmin(flags *cliFlags) (cliAdmin, error) {
	if *flags.remote != "" {
		if *flags.db != "" {
			return nil, fmt.Errorf("--remote and --db are mutually exclusive")
		}
		key := *flags.adminKey
		if key == "" {
			key = os.Getenv("STILE_ADMIN_KEY")
		}
		if key == "" {
			return nil, fmt.Errorf("--admin-key or STILE_ADMIN_KEY is required with --remote")
		}
		return admin.NewClient(*flags.remote, key), nil
	}
	store, err := openStore(flags)
	if err != nil {
		return nil, err
	}
	return &localAdmin{store: store}, nil
}

func openStore(flags *cliFlags) (auth.Store, error) {
	driver := "sqlite"
	if *flags.driver != "" {
		driver = *flags.driver
	}

	// 1. Explicit --dsn (or --db) flag.
	if *flags.db != "" {
		return auth.OpenStore(config.NewDatabaseConfig(driver, *flags.db))
	}
	// 2. --config flag → load config → server.database.
	if *flags.config != "" {
		cfg, err := config.Load(*flags.config)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		dbCfg := cfg.Server().Database()
		if dbCfg.DSN() != "" {
			return auth.OpenStore(dbCfg)
		}
	}
	// 3. Default.
	return auth.OpenStore(config.NewDatabaseConfig(driver, "stile.db"))
}

func runAddCaller(args []string) {
	fs := flag.NewFlagSet("add-caller", flag.ExitOnError)
	name := fs.String("name", "", "unique caller name (required)")
	flags := addCLIFlags(fs)
	fs.Parse(args)

	if *name == "" {
		fmt.Fprintln(os.Stderr, "error: --name is required")
		os.Exit(1)
	}

	adm, err := openAdmin(flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer adm.Close()

	if err := adm.AddCaller(*name); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Caller %q created.\n", *name)
}

func runAddKey(args []string) {
	fs := flag.NewFlagSet("add-key", flag.ExitOnError)
	caller := fs.String("caller", "", "name of existing caller (required)")
	label := fs.String("label", "", "human-readable label for the key")
	flags := addCLIFlags(fs)
	fs.Parse(args)

	if *caller == "" {
		fmt.Fprintln(os.Stderr, "error: --caller is required")
		os.Exit(1)
	}

	adm, err := openAdmin(flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer adm.Close()

	rawKey, err := adm.CreateKey(*caller, *label)
	if err != nil {
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
	flags := addCLIFlags(fs)
	fs.Parse(args)

	if *caller == "" || *role == "" {
		fmt.Fprintln(os.Stderr, "error: --caller and --role are required")
		os.Exit(1)
	}

	// Config-based role validation (local mode only).
	if *flags.config != "" && *flags.remote == "" {
		cfg, err := config.Load(*flags.config)
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

	adm, err := openAdmin(flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer adm.Close()

	if err := adm.AssignRole(*caller, *role); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Role %q assigned to %q.\n", *role, *caller)
}

func runUnassignRole(args []string) {
	fs := flag.NewFlagSet("unassign-role", flag.ExitOnError)
	caller := fs.String("caller", "", "name of existing caller (required)")
	role := fs.String("role", "", "role to unassign (required)")
	flags := addCLIFlags(fs)
	fs.Parse(args)

	if *caller == "" || *role == "" {
		fmt.Fprintln(os.Stderr, "error: --caller and --role are required")
		os.Exit(1)
	}

	adm, err := openAdmin(flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer adm.Close()

	if err := adm.UnassignRole(*caller, *role); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Role %q unassigned from %q.\n", *role, *caller)
}

func runListCallers(args []string) {
	fs := flag.NewFlagSet("list-callers", flag.ExitOnError)
	flags := addCLIFlags(fs)
	fs.Parse(args)

	adm, err := openAdmin(flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer adm.Close()

	callers, err := adm.ListCallers()
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
	flags := addCLIFlags(fs)
	fs.Parse(args)

	if *name == "" {
		fmt.Fprintln(os.Stderr, "error: --name is required")
		os.Exit(1)
	}

	adm, err := openAdmin(flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer adm.Close()

	if !*force {
		count, err := adm.KeyCountForCaller(*name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if count > 0 {
			fmt.Fprintf(os.Stderr, "error: caller %q has %d active key(s); use --force to remove\n", *name, count)
			os.Exit(1)
		}
	}

	if err := adm.DeleteCaller(*name); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Caller %q removed.\n", *name)
}

func runRevokeKey(args []string) {
	fs := flag.NewFlagSet("revoke-key", flag.ExitOnError)
	caller := fs.String("caller", "", "caller who owns the key (required)")
	label := fs.String("label", "", "label of the key to revoke")
	flags := addCLIFlags(fs)
	fs.Parse(args)

	if *caller == "" {
		fmt.Fprintln(os.Stderr, "error: --caller is required")
		os.Exit(1)
	}

	adm, err := openAdmin(flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer adm.Close()

	// If no label given, list keys for the caller.
	if *label == "" {
		keys, err := adm.ListKeys(*caller)
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

	if err := adm.RevokeKey(*caller, *label); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Key %q revoked for caller %q.\n", *label, *caller)
}

func runCacheShow(args []string) {
	fs := flag.NewFlagSet("cache-show", flag.ExitOnError)
	flags := addCLIFlags(fs)
	fs.Parse(args)

	if *flags.remote == "" {
		fmt.Fprintln(os.Stderr, "error: --remote is required (cache is in-memory on the running gateway)")
		os.Exit(1)
	}

	key := *flags.adminKey
	if key == "" {
		key = os.Getenv("STILE_ADMIN_KEY")
	}
	if key == "" {
		fmt.Fprintln(os.Stderr, "error: --admin-key or STILE_ADMIN_KEY is required with --remote")
		os.Exit(1)
	}

	client := admin.NewClient(*flags.remote, key)
	stats, err := client.CacheStats()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "METRIC\tVALUE")
	fmt.Fprintf(w, "Key entries\t%d\n", stats.KeyEntries)
	fmt.Fprintf(w, "Role entries\t%d\n", stats.RoleEntries)
	fmt.Fprintf(w, "Key hits\t%d\n", stats.KeyHits)
	fmt.Fprintf(w, "Key misses\t%d\n", stats.KeyMisses)
	fmt.Fprintf(w, "Role hits\t%d\n", stats.RoleHits)
	fmt.Fprintf(w, "Role misses\t%d\n", stats.RoleMisses)
	fmt.Fprintf(w, "Evictions\t%d\n", stats.Evictions)
	w.Flush()
}

func runCacheFlush(args []string) {
	fs := flag.NewFlagSet("cache-flush", flag.ExitOnError)
	flags := addCLIFlags(fs)
	fs.Parse(args)

	if *flags.remote == "" {
		fmt.Fprintln(os.Stderr, "error: --remote is required (cache is in-memory on the running gateway)")
		os.Exit(1)
	}

	key := *flags.adminKey
	if key == "" {
		key = os.Getenv("STILE_ADMIN_KEY")
	}
	if key == "" {
		fmt.Fprintln(os.Stderr, "error: --admin-key or STILE_ADMIN_KEY is required with --remote")
		os.Exit(1)
	}

	client := admin.NewClient(*flags.remote, key)
	if err := client.CacheFlush(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Cache flushed.")
}

func generateAPIKey() (string, error) {
	return auth.GenerateAPIKey()
}

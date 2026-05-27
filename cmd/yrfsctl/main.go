// yrfsctl is a small CLI for poking at a Yanrong (yrfs) storage system without
// going through the bot. It reuses storage.YanrongBackend so behavior matches
// what the bot would see in production.
//
// Connection info precedence:
//
//	flag > env (YR_BASE_URL / YR_USERNAME / YR_PASSWORD) > rest_storages[<--name>] in config
//
// Examples:
//
//	yrfsctl --config ./config.yaml --name yrfs01 login
//	yrfsctl --base-url https://10.0.0.5 --username admin --password 'pw' info
//	YR_BASE_URL=https://10.0.0.5 YR_USERNAME=admin YR_PASSWORD=pw yrfsctl health
//	yrfsctl --name yrfs01 quota --path /quota_test
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/microyahoo/storage-bot/config"
	"github.com/microyahoo/storage-bot/storage"
)

// Persistent flags shared by every subcommand.
var (
	flagConfig   string
	flagName     string
	flagBaseURL  string
	flagUsername string
	flagPassword string
	flagTimeout  time.Duration
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		// Cobra already prints the error; just exit non-zero.
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "yrfsctl",
		Short:         "Yanrong (yrfs) REST API tester",
		Long:          "yrfsctl exercises a Yanrong storage system using the same backend as the bot.\nCredentials come from --base-url/--username/--password, YR_* env vars, or rest_storages in config.yaml.",
		SilenceUsage:  true, // don't dump usage on every runtime error
		SilenceErrors: false,
	}

	root.PersistentFlags().StringVar(&flagConfig, "config", "", "path to config.yaml (default: $YRFSCTL_CONFIG or ./config.yaml if present)")
	root.PersistentFlags().StringVar(&flagName, "name", "", "rest_storages entry name to use (e.g. yrfs01); required if multiple are configured")
	root.PersistentFlags().StringVar(&flagBaseURL, "base-url", "", "Yanrong base URL (overrides config); env: YR_BASE_URL")
	root.PersistentFlags().StringVar(&flagUsername, "username", "", "Yanrong username (overrides config); env: YR_USERNAME")
	root.PersistentFlags().StringVar(&flagPassword, "password", "", "Yanrong password (overrides config); env: YR_PASSWORD")
	root.PersistentFlags().DurationVar(&flagTimeout, "timeout", 30*time.Second, "request timeout")

	root.AddCommand(
		newInfoCmd(),
		newQuotaCmd(),
	)
	return root
}

func newInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "GET /api/v3/overview — cluster info",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b, ctx, cancel, err := setup()
			if err != nil {
				return err
			}
			defer cancel()
			out, err := b.ClusterInfo(ctx)
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}
}

func newQuotaCmd() *cobra.Command {
	var (
		path  string
		user  string
		scope string
	)
	cmd := &cobra.Command{
		Use:   "quota",
		Short: "list all quotas, look up by --path, or look up a user's dir via --user/--scope",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if path != "" && user != "" {
				return fmt.Errorf("--path and --user are mutually exclusive")
			}
			b, ctx, cancel, err := setup()
			if err != nil {
				return err
			}
			defer cancel()
			var out string
			switch {
			case user != "":
				out, err = b.DirUsageForUser(ctx, user, scope)
			case path != "":
				out, err = b.DirUsage(ctx, path)
			default:
				out, err = b.ListQuotas(ctx)
			}
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "if set, return the quota for this exact path")
	cmd.Flags().StringVar(&user, "user", "", "user name, eg. liangzheng; resolved via configured user prefix")
	cmd.Flags().StringVar(&scope, "scope", "private", "user-prefix scope when --user is set: public or private")
	return cmd
}

// setup resolves credentials and returns a backend + cancellable context.
// The cancel func MUST be called by the caller to release signal handlers.
func setup() (*storage.YanrongBackend, context.Context, context.CancelFunc, error) {
	bURL, user, pass, pubPrefix, privPrefix, err := resolveCreds()
	if err != nil {
		return nil, nil, nil, err
	}
	backend := storage.NewYanrongBackend("yrfsctl", bURL, user, pass)
	backend.SetUserPrefixes(pubPrefix, privPrefix)
	ctx, cancel := signalCtx(flagTimeout)
	return backend, ctx, cancel, nil
}

// resolveCreds picks base_url/username/password using this precedence:
//
//	flag > env > config > error
//
// If --name is empty and the config has exactly one rest_storages entry, that
// one is used. Otherwise --name is required to disambiguate. User-path
// prefixes only come from config (flags/env don't override them).
func resolveCreds() (string, string, string, string, string, error) {
	envURL := os.Getenv("YR_BASE_URL")
	envUser := os.Getenv("YR_USERNAME")
	envPass := os.Getenv("YR_PASSWORD")

	bURL := firstNonEmpty(flagBaseURL, envURL)
	user := firstNonEmpty(flagUsername, envUser)
	pass := firstNonEmpty(flagPassword, envPass)

	if bURL != "" && user != "" && pass != "" {
		return bURL, user, pass, "", "", nil // fully overridden, skip config
	}

	cfgPath := pickConfigPath(flagConfig)
	if cfgPath == "" {
		return "", "", "", "", "", fmt.Errorf("missing credentials: pass --base-url/--username/--password or YR_* env vars, or specify --config")
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("load config %s: %w", cfgPath, err)
	}
	if len(cfg.RESTStorages) == 0 {
		return "", "", "", "", "", fmt.Errorf("config %s has no rest_storages entries", cfgPath)
	}

	rs, err := pickStorage(cfg.RESTStorages, flagName)
	if err != nil {
		return "", "", "", "", "", err
	}

	bURL = firstNonEmpty(bURL, rs.BaseURL)
	user = firstNonEmpty(user, rs.Username)
	pass = firstNonEmpty(pass, rs.Password)

	if bURL == "" || user == "" || pass == "" {
		return "", "", "", "", "", fmt.Errorf("incomplete credentials after merging config: base_url=%q username=%q password=%q", bURL, user, redact(pass))
	}
	return bURL, user, pass, rs.PublicUserPrefix, rs.PrivateUserPrefix, nil
}

func pickConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv("YRFSCTL_CONFIG"); env != "" {
		return env
	}
	if _, err := os.Stat("config.yaml"); err == nil {
		return "config.yaml"
	}
	return ""
}

func pickStorage(m map[string]*config.RESTStorageConfig, name string) (*config.RESTStorageConfig, error) {
	if name != "" {
		rs, ok := m[name]
		if !ok {
			return nil, fmt.Errorf("rest_storages %q not found; available: %s", name, strings.Join(sortedKeys(m), ", "))
		}
		return rs, nil
	}
	if len(m) == 1 {
		for _, rs := range m {
			return rs, nil
		}
	}
	return nil, fmt.Errorf("multiple rest_storages configured; pass --name <one of: %s>", strings.Join(sortedKeys(m), ", "))
}

func sortedKeys(m map[string]*config.RESTStorageConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func signalCtx(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	return ctx, func() { stop(); cancel() }
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func redact(s string) string {
	if s == "" {
		return ""
	}
	return "***"
}

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vaughanb/agent-context-go/internal/installer"
)

// envList collects repeated --env KEY=VALUE flags.
type envList []string

func (e *envList) String() string { return strings.Join(*e, ",") }
func (e *envList) Set(v string) error {
	if !strings.Contains(v, "=") {
		return fmt.Errorf("expected KEY=VALUE, got %q", v)
	}
	*e = append(*e, v)
	return nil
}

// runInstall registers (or, when uninstall is true, removes) this binary in the
// selected agents' configs.
func runInstall(args []string, uninstall bool) error {
	action := "install"
	if uninstall {
		action = "uninstall"
	}
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	var agentsCSV, name string
	var dryRun, all bool
	var envs envList
	fs.StringVar(&agentsCSV, "agents", "", "comma-separated agent ids to target (default: all detected)")
	fs.StringVar(&name, "name", "agent-context-go", "server name to register")
	fs.BoolVar(&dryRun, "dry-run", false, "show changes without writing")
	fs.BoolVar(&all, "all", false, "target all supported agents, not just detected ones")
	fs.Var(&envs, "env", "environment variable KEY=VALUE to pass to the server (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	registry := installer.New()
	targets, err := selectTargets(registry, agentsCSV, all)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		fmt.Printf("No agents detected. Re-run with --agents <id,...> or --all.\nSupported: %s\n",
			strings.Join(installer.IDs(registry), ", "))
		return nil
	}

	var results []installer.Result
	if uninstall {
		results = installer.Uninstall(targets, name, dryRun)
	} else {
		exe, err := selfPath()
		if err != nil {
			return err
		}
		srv := installer.Server{Name: name, Command: exe, Env: parseEnv(envs)}
		fmt.Printf("Registering %q → %s\n\n", name, exe)
		results = installer.Install(targets, srv, dryRun)
	}

	return reportResults(results, dryRun)
}

// runList prints every supported agent and whether it is detected.
func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Println("Supported agents (● = detected on this machine):")
	for _, a := range installer.New() {
		mark := "○"
		if a.Detected() {
			mark = "●"
		}
		fmt.Printf("  %s  %-14s %-20s %s\n", mark, a.ID(), a.Name(), a.Location())
	}
	return nil
}

// selectTargets resolves which agents to act on from the flags.
func selectTargets(registry []installer.Agent, agentsCSV string, all bool) ([]installer.Agent, error) {
	switch {
	case agentsCSV != "":
		ids := splitCSV(agentsCSV)
		sel, unknown := installer.Select(registry, ids)
		if len(unknown) > 0 {
			return nil, fmt.Errorf("unknown agent(s): %s (supported: %s)",
				strings.Join(unknown, ", "), strings.Join(installer.IDs(registry), ", "))
		}
		return sel, nil
	case all:
		return registry, nil
	default:
		return installer.Detected(registry), nil
	}
}

// reportResults prints a per-agent summary and returns an error if any agent
// failed.
func reportResults(results []installer.Result, dryRun bool) error {
	var failed int
	for _, r := range results {
		switch {
		case r.Err != nil:
			failed++
			fmt.Printf("  ✗ %-18s %v\n", r.AgentName, r.Err)
		default:
			fmt.Printf("  %s %-18s %s\n", actionMark(r.Action), r.AgentName, describe(r))
		}
	}
	if dryRun {
		fmt.Println("\nDry run — no files were changed.")
	}
	if failed > 0 {
		return fmt.Errorf("%d agent(s) failed", failed)
	}
	return nil
}

func describe(r installer.Result) string {
	parts := []string{string(r.Action)}
	if r.Location != "" {
		parts = append(parts, "→ "+r.Location)
	}
	line := strings.Join(parts, " ")
	if r.Backup != "" {
		line += " (backup: " + filepath.Base(r.Backup) + ")"
	}
	if r.Note != "" {
		line += "\n      " + strings.ReplaceAll(r.Note, "\n", "\n      ")
	}
	return line
}

func actionMark(a installer.Action) string {
	switch a {
	case installer.ActionInstalled, installer.ActionUpdated, installer.ActionRemoved:
		return "✓"
	case installer.ActionSkipped:
		return "–"
	default:
		return " "
	}
}

// selfPath returns the absolute path to this executable, resolving symlinks so
// the registered command is stable.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate this executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

func parseEnv(envs envList) map[string]string {
	if len(envs) == 0 {
		return nil
	}
	m := make(map[string]string, len(envs))
	for _, kv := range envs {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

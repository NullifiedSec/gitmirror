package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/NullifiedSec/gitmirror/internal/approval"
	"github.com/NullifiedSec/gitmirror/internal/config"
	"github.com/NullifiedSec/gitmirror/internal/syncer"
)

const defaultConfigPath = "/etc/gitmirror/gitmirror.toml"

type App struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

func New() *App { return &App{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr} }

// Run executes operator CLI commands. It returns handled=false when the caller
// should start the daemon (no command or the explicit "serve" command).
func (a *App) Run(args []string) (handled bool, exitCode int) {
	configPath, rest, err := parseGlobal(args)
	if err != nil {
		fmt.Fprintln(a.ErrOut, err)
		return true, 2
	}
	if len(rest) == 0 || rest[0] == "serve" {
		return false, 0
	}

	var runErr error
	switch rest[0] {
	case "help", "-h", "--help":
		a.usage()
		return true, 0
	case "config":
		runErr = a.runConfig(configPath, rest[1:])
	case "pair", "pairs":
		runErr = a.runPair(configPath, rest[1:])
	case "approvals", "approval":
		runErr = a.runApprovals(configPath, rest[1:])
	case "status":
		runErr = a.runStatus(configPath)
	case "sync":
		runErr = a.runSync(configPath, rest[1:])
	default:
		runErr = fmt.Errorf("unknown command %q", rest[0])
	}
	if runErr != nil {
		fmt.Fprintf(a.ErrOut, "gitmirror: %v\n", runErr)
		return true, 1
	}
	return true, 0
}

// ServeConfigPath resolves global options for daemon mode and rejects stray
// positional arguments after the optional serve command.
func ServeConfigPath(args []string) (string, error) {
	path, rest, err := parseGlobal(args)
	if err != nil {
		return "", err
	}
	if len(rest) == 0 {
		return path, nil
	}
	if rest[0] != "serve" || len(rest) != 1 {
		return "", fmt.Errorf("usage: gitmirror [--config PATH] serve")
	}
	return path, nil
}

func parseGlobal(args []string) (string, []string, error) {
	configPath := defaultConfigPath
	for len(args) > 0 {
		switch args[0] {
		case "--config", "-config", "-c":
			if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
				return "", nil, fmt.Errorf("%s requires a path", args[0])
			}
			configPath = args[1]
			args = args[2:]
		case "--":
			return configPath, args[1:], nil
		default:
			return configPath, args, nil
		}
	}
	return configPath, nil, nil
}

func (a *App) usage() {
	fmt.Fprintln(a.Out, `gitmirror - repository synchronization service

Usage:
  gitmirror [--config PATH] serve
  gitmirror [--config PATH] status
  gitmirror [--config PATH] sync
  gitmirror [--config PATH] config validate
  gitmirror [--config PATH] pair list
  gitmirror [--config PATH] pair add
  gitmirror [--config PATH] approvals list
  gitmirror [--config PATH] approvals show <id>
  gitmirror [--config PATH] approvals approve <id>
  gitmirror [--config PATH] approvals reject <id>

The installed config defaults to /etc/gitmirror/gitmirror.toml.`)
}

func (a *App) runConfig(path string, args []string) error {
	if len(args) != 1 || args[0] != "validate" {
		return fmt.Errorf("usage: gitmirror [--config PATH] config validate")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "OK: %s (%d pairs)\n", path, len(cfg.Pairs))
	return nil
}

func (a *App) runPair(path string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gitmirror pair <list|add>")
	}
	switch args[0] {
	case "list":
		cfg, err := config.Load(path)
		if err != nil {
			return err
		}
		for _, p := range cfg.Pairs {
			fmt.Fprintf(a.Out, "%s\t%s/%s <-> %s/%s\n", p.Name, normalizedProvider(p.Left.Provider), p.Left.FullName, normalizedProvider(p.Right.Provider), p.Right.FullName)
		}
		return nil
	case "add":
		if len(args) != 1 {
			return fmt.Errorf("usage: gitmirror [--config PATH] pair add")
		}
		return a.addPairInteractive(path)
	default:
		return fmt.Errorf("unknown pair command %q", args[0])
	}
}

func (a *App) addPairInteractive(path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	r := bufio.NewReader(a.In)
	fmt.Fprintln(a.Out, "Add repository pair")
	name, err := promptRequired(r, a.Out, "Pair name")
	if err != nil {
		return err
	}
	left, err := promptRepo(r, a.Out, "Left")
	if err != nil {
		return err
	}
	right, err := promptRepo(r, a.Out, "Right")
	if err != nil {
		return err
	}
	cfg.Pairs = append(cfg.Pairs, config.Pair{Name: name, Left: left, Right: right})
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("new pair is invalid: %w", err)
	}
	fmt.Fprintln(a.Out)
	fmt.Fprintf(a.Out, "Will add %s: %s/%s <-> %s/%s\n", name, normalizedProvider(left.Provider), left.FullName, normalizedProvider(right.Provider), right.FullName)
	ok, err := promptYesNo(r, a.Out, "Write configuration?", false)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("cancelled")
	}
	if err := config.WriteTOML(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Added pair %q to %s\n", name, path)
	return nil
}

func promptRepo(r *bufio.Reader, out io.Writer, side string) (config.Repo, error) {
	fmt.Fprintf(out, "\n%s repository\n", side)
	provider, err := promptDefault(r, out, "Provider", config.ProviderGitHub)
	if err != nil {
		return config.Repo{}, err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	fullName, err := promptRequired(r, out, "Full name (owner/repo)")
	if err != nil {
		return config.Repo{}, err
	}
	defaultURL := ""
	if provider == config.ProviderGitHub {
		defaultURL = "git@github.com:" + fullName + ".git"
	}
	url, err := promptDefault(r, out, "Git URL", defaultURL)
	if err != nil {
		return config.Repo{}, err
	}
	polling, err := promptYesNo(r, out, "Enable polling?", false)
	if err != nil {
		return config.Repo{}, err
	}
	frequency := 0
	if polling {
		v, err := promptDefault(r, out, "Polling frequency seconds", strconv.Itoa(config.DefaultPollingFrequency))
		if err != nil {
			return config.Repo{}, err
		}
		frequency, err = strconv.Atoi(v)
		if err != nil {
			return config.Repo{}, fmt.Errorf("polling frequency must be an integer")
		}
	}
	hil, err := promptDefault(r, out, "HIL protected branches (comma separated, blank for none)", "")
	if err != nil {
		return config.Repo{}, err
	}
	var branches []string
	for _, branch := range strings.Split(hil, ",") {
		if branch = strings.TrimSpace(branch); branch != "" {
			branches = append(branches, branch)
		}
	}
	return config.Repo{Provider: provider, FullName: fullName, URL: url, Polling: polling, PollingFrequency: frequency, HumanInLoopBranches: branches}, nil
}

func (a *App) runApprovals(configPath string, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	store := approval.New(cfg.DataDir)
	if len(args) == 0 || args[0] == "list" {
		items, err := store.ListPending()
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Fprintln(a.Out, "No pending approvals.")
			return nil
		}
		for _, item := range items {
			fmt.Fprintf(a.Out, "%s\t%s\t%s\t%s -> %s\n", item.ID, item.TargetFullName, strings.TrimPrefix(item.Ref, "refs/heads/"), short(item.Before), short(item.After))
		}
		return nil
	}
	if len(args) != 2 {
		return fmt.Errorf("usage: gitmirror approvals <show|approve|reject> <id>")
	}
	id := args[1]
	switch args[0] {
	case "show":
		req, err := store.Load(id)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(req, "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return nil
	case "approve":
		if err := syncer.New(cfg).Approve(context.Background(), id); err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "Approved %s\n", id)
		return nil
	case "reject":
		if err := store.Reject(id); err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "Rejected %s\n", id)
		return nil
	default:
		return fmt.Errorf("unknown approvals command %q", args[0])
	}
}

func (a *App) runStatus(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	pendingApprovals, err := approval.New(cfg.DataDir).ListPending()
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Config: %s\nPairs: %d\nPending approvals: %d\n", configPath, len(cfg.Pairs), len(pendingApprovals))
	for _, state := range []string{"pending", "processing", "failed"} {
		n, err := countJSON(filepath.Join(cfg.DataDir, "queue", state))
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "Queue %-10s %d\n", state+":", n)
	}
	return nil
}

func promptRequired(r *bufio.Reader, out io.Writer, label string) (string, error) {
	for {
		v, err := promptDefault(r, out, label, "")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), nil
		}
		fmt.Fprintln(out, "Value is required.")
	}
}

func promptDefault(r *bufio.Reader, out io.Writer, label, def string) (string, error) {
	if def == "" {
		fmt.Fprintf(out, "%s: ", label)
	} else {
		fmt.Fprintf(out, "%s [%s]: ", label, def)
	}
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = def
	}
	return line, nil
}

func promptYesNo(r *bufio.Reader, out io.Writer, label string, def bool) (bool, error) {
	defText := "y/N"
	if def {
		defText = "Y/n"
	}
	for {
		fmt.Fprintf(out, "%s [%s]: ", label, defText)
		line, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(out, "Please answer yes or no.")
		}
	}
}

func countJSON(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			n++
		}
	}
	return n, nil
}

func normalizedProvider(v string) string {
	if strings.TrimSpace(v) == "" {
		return config.ProviderGitHub
	}
	return strings.ToLower(strings.TrimSpace(v))
}

func short(v string) string {
	if len(v) > 12 {
		return v[:12]
	}
	return v
}

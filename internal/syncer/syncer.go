package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/NullifiedSec/gitmirror/internal/config"
	"github.com/NullifiedSec/gitmirror/internal/queue"
)

type Syncer struct {
	cfg     config.Config
	dataDir string
	secrets []string
}

type pushPayload struct {
	Ref     string `json:"ref"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Deleted bool   `json:"deleted"`
	Repo    struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func New(cfg config.Config) *Syncer {
	var secrets []string
	for _, p := range cfg.Pairs {
		secrets = append(secrets, p.Left.URL, p.Right.URL)
	}
	return &Syncer{cfg: cfg, dataDir: cfg.DataDir, secrets: secrets}
}

func (s *Syncer) Process(ctx context.Context, e queue.Event) error {
	if e.Type != "push" {
		return nil
	}
	var p pushPayload
	if err := json.Unmarshal(e.Body, &p); err != nil {
		return fmt.Errorf("decode push payload: %w", err)
	}
	const heads = "refs/heads/"
	if !strings.HasPrefix(p.Ref, heads) {
		return nil
	}
	branch := strings.TrimPrefix(p.Ref, heads)
	if err := checkBranch(ctx, branch); err != nil {
		return err
	}
	pair, source, _, ok := s.findPair(p.Repo.FullName)
	if !ok {
		return nil
	}
	repoDir := filepath.Join(s.dataDir, "repos", safePairName(pair.Name)+".git")
	if err := s.ensureBare(ctx, repoDir, pair); err != nil {
		return err
	}
	sourceRemote, targetRemote := "left", "right"
	if strings.EqualFold(source.FullName, pair.Right.FullName) {
		sourceRemote, targetRemote = targetRemote, sourceRemote
	}
	if p.Deleted {
		return s.deleteBranch(ctx, repoDir, sourceRemote, targetRemote, p.Ref, p.Before)
	}
	return s.updateBranch(ctx, repoDir, sourceRemote, targetRemote, p.Ref)
}

func (s *Syncer) findPair(fullName string) (config.Pair, config.Repo, config.Repo, bool) {
	for _, p := range s.cfg.Pairs {
		if strings.EqualFold(fullName, p.Left.FullName) {
			return p, p.Left, p.Right, true
		}
		if strings.EqualFold(fullName, p.Right.FullName) {
			return p, p.Right, p.Left, true
		}
	}
	return config.Pair{}, config.Repo{}, config.Repo{}, false
}

func (s *Syncer) ensureBare(ctx context.Context, repoDir string, pair config.Pair) error {
	if err := os.MkdirAll(filepath.Dir(repoDir), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(repoDir, "HEAD")); errors.Is(err, os.ErrNotExist) {
		if _, err := run(ctx, "", s.secrets, "git", "init", "--bare", repoDir); err != nil {
			return fmt.Errorf("initialize local mirror: %w", err)
		}
	} else if err != nil {
		return err
	}
	for name, url := range map[string]string{"left": pair.Left.URL, "right": pair.Right.URL} {
		if _, err := run(ctx, repoDir, s.secrets, "git", "remote", "get-url", name); err != nil {
			if _, addErr := run(ctx, repoDir, s.secrets, "git", "remote", "add", name, url); addErr != nil {
				return fmt.Errorf("configure %s remote: %w", name, addErr)
			}
		} else if _, err := run(ctx, repoDir, s.secrets, "git", "remote", "set-url", name, url); err != nil {
			return fmt.Errorf("update %s remote: %w", name, err)
		}
	}
	return nil
}

func (s *Syncer) updateBranch(ctx context.Context, repoDir, source, target, ref string) error {
	sourceSHA, exists, err := s.remoteSHA(ctx, repoDir, source, ref)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if _, err := run(ctx, repoDir, s.secrets, "git", "fetch", "--no-tags", source, "+"+ref+":refs/gitmirror/source); err != nil {
		return fmt.Errorf("fetch source branch: %w", err)
	}
	targetSHA, targetExists, err := s.remoteSHA(ctx, repoDir, target, ref)
	if err != nil {
		return err
	}
	if !targetExists {
		if _, err := run(ctx, repoDir, s.secrets, "git", "push", target, "refs/gitmirror/source:"+ref); err != nil {
			return fmt.Errorf("create target branch: %w", err)
		}
		return nil
	}
	if targetSHA == sourceSHA {
		return nil
	}
	if _, err := run(ctx, repoDir, s.secrets, "git", "fetch", "--no-tags", target, "+"+ref+":refs/gitmirror/target); err != nil {
		return fmt.Errorf("fetch target branch: %w", err)
	}
	targetBehind, err := isAncestor(ctx, repoDir, targetSHA, sourceSHA)
	if err != nil {
		return err
	}
	if targetBehind {
		if _, err := run(ctx, repoDir, s.secrets, "git", "push", target, "refs/gitmirror/source:"+ref); err != nil {
			return fmt.Errorf("fast-forward target branch: %w", err)
		}
		return nil
	}
	sourceBehind, err := isAncestor(ctx, repoDir, sourceSHA, targetSHA)
	if err != nil {
		return err
	}
	if sourceBehind {
		return nil
	}
	return fmt.Errorf("branch %s diverged between %s and %s; refusing destructive update", ref, source, target)
}

func (s *Syncer) deleteBranch(ctx context.Context, repoDir, source, target, ref, before string) error {
	if _, exists, err := s.remoteSHA(ctx, repoDir, source, ref); err != nil {
		return err
	} else if exists {
		return nil
	}
	targetSHA, exists, err := s.remoteSHA(ctx, repoDir, target, ref)
	if err != nil || !exists {
		return err
	}
	if before == "" || isZeroSHA(before) || targetSHA != before {
		return fmt.Errorf("refusing to delete %s: target moved from expected source SHA", ref)
	}
	if _, err := run(ctx, repoDir, s.secrets, "git", "push", target, ":"+ref); err != nil {
		return fmt.Errorf("delete target branch: %w", err)
	}
	return nil
}

func (s *Syncer) remoteSHA(ctx context.Context, repoDir, remote, ref string) (string, bool, error) {
	out, err := run(ctx, repoDir, s.secrets, "git", "ls-remote", "--exit-code", remote, ref)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 2 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read remote ref %s: %w", ref, err)
	}
	fields := strings.Fields(out)
	if len(fields) < 1 {
		return "", false, nil
	}
	return fields[0], true, nil
}

func isAncestor(ctx context.Context, repoDir, ancestor, descendant string) (bool, error) {
	_, err := run(ctx, repoDir, nil, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check commit ancestry: %w", err)
}

func checkBranch(ctx context.Context, branch string) error {
	if branch == "" {
		return fmt.Errorf("empty branch name")
	}
	if _, err := run(ctx, "", nil, "git", "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("invalid branch name: %w", err)
	}
	return nil
}

func run(ctx context.Context, dir string, secrets []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	text := string(out)
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED_REMOTE]")
		}
	}
	if err != nil {
		return text, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(text))
	}
	return text, nil
}

func safePairName(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", "..", "_")
	return r.Replace(s)
}

func isZeroSHA(s string) bool { return strings.Trim(s, "0") == "" }

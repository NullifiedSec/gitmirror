package syncer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NullifiedSec/gitmirror/internal/approval"
	"github.com/NullifiedSec/gitmirror/internal/config"
	"github.com/NullifiedSec/gitmirror/internal/queue"
)

type Syncer struct {
	cfg       config.Config
	dataDir   string
	secrets   []string
	approvals *approval.Store
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
	return &Syncer{cfg: cfg, dataDir: cfg.DataDir, secrets: secrets, approvals: approval.New(cfg.DataDir)}
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
	provider := normalizeProvider(e.Provider)
	pair, sourceRepo, targetRepo, ok := s.findPair(provider, p.Repo.FullName)
	if !ok {
		return nil
	}
	repoDir := filepath.Join(s.dataDir, "repos", safePairName(pair.Name)+".git")
	if err := s.ensureBare(ctx, repoDir, pair); err != nil {
		return err
	}
	sourceRemote, targetRemote := remoteNames(pair, sourceRepo)
	if p.Deleted {
		return s.deleteBranch(ctx, repoDir, pair, sourceRepo, targetRepo, sourceRemote, targetRemote, p.Ref, p.Before)
	}
	return s.updateBranch(ctx, repoDir, pair, sourceRepo, targetRepo, sourceRemote, targetRemote, p.Ref)
}

func (s *Syncer) Approve(ctx context.Context, id string) error {
	req, err := s.approvals.Load(id)
	if err != nil {
		return fmt.Errorf("load approval %s: %w", id, err)
	}
	pair, ok := s.findPairByName(req.Pair)
	if !ok {
		return fmt.Errorf("approval %s references unknown pair %q", id, req.Pair)
	}
	sourceRepo, targetRepo, ok := approvalRepos(pair, req)
	if !ok {
		return fmt.Errorf("approval %s no longer matches configured repositories", id)
	}
	branch := strings.TrimPrefix(req.Ref, "refs/heads/")
	if !config.RequiresHumanApproval(targetRepo, branch) {
		return fmt.Errorf("approval %s target branch %s is no longer configured for human approval", id, branch)
	}
	repoDir := filepath.Join(s.dataDir, "repos", safePairName(pair.Name)+".git")
	if err := s.ensureBare(ctx, repoDir, pair); err != nil {
		return err
	}
	sourceRemote, targetRemote := remoteNames(pair, sourceRepo)

	sourceSHA, sourceExists, err := s.remoteSHA(ctx, repoDir, sourceRemote, req.Ref)
	if err != nil {
		return err
	}
	targetSHA, targetExists, err := s.remoteSHA(ctx, repoDir, targetRemote, req.Ref)
	if err != nil {
		return err
	}
	if req.Delete {
		if sourceExists {
			return fmt.Errorf("approval %s expired: source branch exists again", id)
		}
		if !targetExists || targetSHA != req.Before {
			return fmt.Errorf("approval %s expired: target branch moved", id)
		}
		if err := s.strictDelete(ctx, repoDir, targetRemote, req.Ref); err != nil {
			return err
		}
		return s.approvals.Complete(id)
	}

	if !sourceExists || sourceSHA != req.After {
		return fmt.Errorf("approval %s expired: source branch moved", id)
	}
	if isZeroSHA(req.Before) {
		if targetExists {
			return fmt.Errorf("approval %s expired: target branch was created", id)
		}
	} else if !targetExists || targetSHA != req.Before {
		return fmt.Errorf("approval %s expired: target branch moved", id)
	}
	if _, err := run(ctx, repoDir, s.secrets, "git", "fetch", "--no-tags", sourceRemote, "+"+req.Ref+":refs/gitmirror/source"); err != nil {
		return fmt.Errorf("fetch approved source branch: %w", err)
	}
	if targetExists {
		if _, err := run(ctx, repoDir, s.secrets, "git", "fetch", "--no-tags", targetRemote, "+"+req.Ref+":refs/gitmirror/target"); err != nil {
			return fmt.Errorf("fetch approved target branch: %w", err)
		}
		behind, err := isAncestor(ctx, repoDir, targetSHA, sourceSHA)
		if err != nil {
			return err
		}
		if !behind {
			return fmt.Errorf("approval %s expired: approved update is no longer a fast-forward", id)
		}
	}
	if err := s.pushOrQuarantine(ctx, repoDir, targetRemote, req.Ref); err != nil {
		return err
	}
	return s.approvals.Complete(id)
}

func (s *Syncer) findPair(provider, fullName string) (config.Pair, config.Repo, config.Repo, bool) {
	for _, p := range s.cfg.Pairs {
		if repoMatches(p.Left, provider, fullName) {
			return p, p.Left, p.Right, true
		}
		if repoMatches(p.Right, provider, fullName) {
			return p, p.Right, p.Left, true
		}
	}
	return config.Pair{}, config.Repo{}, config.Repo{}, false
}

func (s *Syncer) findPairByName(name string) (config.Pair, bool) {
	for _, p := range s.cfg.Pairs {
		if p.Name == name {
			return p, true
		}
	}
	return config.Pair{}, false
}

func repoMatches(repo config.Repo, provider, fullName string) bool {
	return normalizeProvider(repo.Provider) == normalizeProvider(provider) && strings.EqualFold(repo.FullName, fullName)
}

func sameRepo(a, b config.Repo) bool {
	return repoMatches(a, b.Provider, b.FullName)
}

func normalizeProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return config.ProviderGitHub
	}
	return provider
}

func remoteNames(pair config.Pair, source config.Repo) (string, string) {
	if sameRepo(source, pair.Right) {
		return "right", "left"
	}
	return "left", "right"
}

func approvalRepos(pair config.Pair, req approval.Request) (config.Repo, config.Repo, bool) {
	if repoMatches(pair.Left, req.SourceProvider, req.SourceFullName) && repoMatches(pair.Right, req.TargetProvider, req.TargetFullName) {
		return pair.Left, pair.Right, true
	}
	if repoMatches(pair.Right, req.SourceProvider, req.SourceFullName) && repoMatches(pair.Left, req.TargetProvider, req.TargetFullName) {
		return pair.Right, pair.Left, true
	}
	return config.Repo{}, config.Repo{}, false
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

func (s *Syncer) updateBranch(ctx context.Context, repoDir string, pair config.Pair, sourceRepo, targetRepo config.Repo, source, target, ref string) error {
	sourceSHA, exists, err := s.remoteSHA(ctx, repoDir, source, ref)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if _, err := run(ctx, repoDir, s.secrets, "git", "fetch", "--no-tags", source, "+"+ref+":refs/gitmirror/source"); err != nil {
		return fmt.Errorf("fetch source branch: %w", err)
	}
	targetSHA, targetExists, err := s.remoteSHA(ctx, repoDir, target, ref)
	if err != nil {
		return err
	}
	if targetExists && targetSHA == sourceSHA {
		return nil
	}
	if targetExists {
		if _, err := run(ctx, repoDir, s.secrets, "git", "fetch", "--no-tags", target, "+"+ref+":refs/gitmirror/target"); err != nil {
			return fmt.Errorf("fetch target branch: %w", err)
		}
		targetBehind, err := isAncestor(ctx, repoDir, targetSHA, sourceSHA)
		if err != nil {
			return err
		}
		if !targetBehind {
			sourceBehind, err := isAncestor(ctx, repoDir, sourceSHA, targetSHA)
			if err != nil {
				return err
			}
			if sourceBehind {
				return nil
			}
			return s.quarantine(ctx, repoDir, target, ref, "diverged")
		}
	}
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if config.RequiresHumanApproval(targetRepo, branch) {
		before := targetSHA
		if !targetExists {
			before = zeroSHA40()
		}
		return s.requestApproval(pair, sourceRepo, targetRepo, ref, before, sourceSHA, false)
	}
	return s.pushOrQuarantine(ctx, repoDir, target, ref)
}

func (s *Syncer) deleteBranch(ctx context.Context, repoDir string, pair config.Pair, sourceRepo, targetRepo config.Repo, source, target, ref, before string) error {
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
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if config.RequiresHumanApproval(targetRepo, branch) {
		return s.requestApproval(pair, sourceRepo, targetRepo, ref, targetSHA, zeroSHA40(), true)
	}
	return s.strictDelete(ctx, repoDir, target, ref)
}

func (s *Syncer) requestApproval(pair config.Pair, sourceRepo, targetRepo config.Repo, ref, before, after string, deleteRef bool) error {
	req := approval.Request{
		ID:             approvalID(pair.Name, targetRepo, ref, before, after, deleteRef),
		Pair:           pair.Name,
		SourceProvider: normalizeProvider(sourceRepo.Provider),
		SourceFullName: sourceRepo.FullName,
		TargetProvider: normalizeProvider(targetRepo.Provider),
		TargetFullName: targetRepo.FullName,
		Ref:            ref,
		Before:         before,
		After:          after,
		Delete:         deleteRef,
	}
	created, err := s.approvals.Create(req)
	if err != nil {
		return fmt.Errorf("create human approval request: %w", err)
	}
	log.Printf("HIL approval required: id=%s pair=%s target=%s ref=%s %s -> %s delete=%t", created.ID, pair.Name, targetRepo.FullName, ref, shortSHA(before), shortSHA(after), deleteRef)
	return nil
}

func (s *Syncer) pushOrQuarantine(ctx context.Context, repoDir, target, ref string) error {
	safe, reason := s.preflightPush(ctx, repoDir, target, "refs/gitmirror/source:"+ref, false)
	if !safe {
		return s.quarantine(ctx, repoDir, target, ref, "preflight-"+reason)
	}
	if err := s.actualPush(ctx, repoDir, target, "refs/gitmirror/source:"+ref, false); err != nil {
		if quarantineErr := s.quarantine(ctx, repoDir, target, ref, "push-failed"); quarantineErr != nil {
			return fmt.Errorf("target push failed (%v) and quarantine failed: %w", err, quarantineErr)
		}
		return nil
	}
	return nil
}

func (s *Syncer) strictDelete(ctx context.Context, repoDir, target, ref string) error {
	safe, reason := s.preflightPush(ctx, repoDir, target, ":"+ref, true)
	if !safe {
		return fmt.Errorf("refusing deletion %s: push preflight complained: %s", ref, reason)
	}
	if err := s.actualPush(ctx, repoDir, target, ":"+ref, true); err != nil {
		return fmt.Errorf("delete target branch: %w", err)
	}
	return nil
}

func (s *Syncer) preflightPush(ctx context.Context, repoDir, remote, refspec string, deletion bool) (bool, string) {
	stdout, stderr, err := runSplit(ctx, repoDir, s.secrets, "git", "push", "--dry-run", "--porcelain", remote, refspec)
	if err != nil {
		return false, "command-failed"
	}
	if strings.TrimSpace(stderr) != "" {
		return false, "stderr-output"
	}
	if !porcelainSafe(stdout, deletion) {
		return false, "unexpected-status"
	}
	return true, ""
}

func (s *Syncer) actualPush(ctx context.Context, repoDir, remote, refspec string, deletion bool) error {
	stdout, _, err := runSplit(ctx, repoDir, s.secrets, "git", "push", "--porcelain", remote, refspec)
	if err != nil {
		return err
	}
	if !porcelainSafe(stdout, deletion) {
		return fmt.Errorf("push returned unexpected porcelain status")
	}
	return nil
}

func porcelainSafe(out string, deletion bool) bool {
	seen := false
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "\t") {
			continue
		}
		seen = true
		flag := line[0]
		if deletion {
			if flag != '-' && flag != '=' {
				return false
			}
			continue
		}
		if flag != ' ' && flag != '*' && flag != '=' {
			return false
		}
	}
	return seen
}

func (s *Syncer) quarantine(ctx context.Context, repoDir, target, originalRef, reason string) error {
	branch := strings.TrimPrefix(originalRef, "refs/heads/")
	for i := 0; i < 3; i++ {
		suffix, err := randomSuffix()
		if err != nil {
			return err
		}
		name := fmt.Sprintf("gitmirror/quarantine/%s-%d-%s", sanitizeBranchPart(branch), time.Now().UTC().Unix(), suffix)
		ref := "refs/heads/" + name
		stdout, stderr, err := runSplit(ctx, repoDir, s.secrets, "git", "push", "--porcelain", target, "refs/gitmirror/source:"+ref)
		if err == nil && strings.TrimSpace(stderr) == "" && porcelainSafe(stdout, false) {
			log.Printf("unsafe update diverted to %s (%s)", ref, reason)
			return nil
		}
	}
	return fmt.Errorf("unable to create quarantine branch for %s after unsafe update: %s", originalRef, reason)
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
	stdout, stderr, err := runSplit(ctx, dir, secrets, name, args...)
	text := strings.TrimSpace(strings.Join([]string{stdout, stderr}, "\n"))
	if err != nil {
		return text, fmt.Errorf("%s: %w: %s", name, err, text)
	}
	return text, nil
}

func runSplit(ctx context.Context, dir string, secrets []string, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return redact(stdout.String(), secrets), redact(stderr.String(), secrets), err
}

func redact(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED_REMOTE]")
		}
	}
	return text
}

func safePairName(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:12])
}

func approvalID(pair string, target config.Repo, ref, before, after string, deleteRef bool) string {
	value := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t", pair, normalizeProvider(target.Provider), strings.ToLower(target.FullName), ref, before, after, deleteRef)
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("hil-%x", sum[:12])
}

func randomSuffix() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func sanitizeBranchPart(s string) string {
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, " ", "-")
	if s == "" {
		return "branch"
	}
	return s
}

func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func zeroSHA40() string { return strings.Repeat("0", 40) }

func isZeroSHA(s string) bool { return strings.Trim(s, "0") == "" }

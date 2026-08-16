package syncer

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NullifiedSec/gitmirror/internal/config"
	"github.com/NullifiedSec/gitmirror/internal/queue"
)

func TestFastForwardAndDivergenceQuarantine(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	left := filepath.Join(root, "left.git")
	right := filepath.Join(root, "right.git")
	mustGit(t, "", "init", "--bare", left)
	mustGit(t, "", "init", "--bare", right)

	work := filepath.Join(root, "work")
	mustGit(t, "", "init", "-b", "main", work)
	mustGit(t, work, "config", "user.email", "test@example.invalid")
	mustGit(t, work, "config", "user.name", "gitmirror test")
	mustWrite(t, filepath.Join(work, "file.txt"), "one\n")
	mustGit(t, work, "add", "file.txt")
	mustGit(t, work, "commit", "-m", "one")
	mustGit(t, work, "remote", "add", "origin", left)
	mustGit(t, work, "push", "origin", "main")
	sha1 := gitOut(t, work, "rev-parse", "HEAD")

	s := New(testConfig(root, left, right))
	if err := s.Process(ctx, pushEvent("acme/left", "refs/heads/main", strings.Repeat("0", 40), sha1, false)); err != nil {
		t.Fatal(err)
	}
	if got := remoteRef(t, right, "refs/heads/main"); got != sha1 {
		t.Fatalf("target sha = %s, want %s", got, sha1)
	}

	mustWrite(t, filepath.Join(work, "file.txt"), "two\n")
	mustGit(t, work, "add", "file.txt")
	mustGit(t, work, "commit", "-m", "two")
	mustGit(t, work, "push", "origin", "main")
	sha2 := gitOut(t, work, "rev-parse", "HEAD")
	if err := s.Process(ctx, pushEvent("acme/left", "refs/heads/main", sha1, sha2, false)); err != nil {
		t.Fatal(err)
	}
	if got := remoteRef(t, right, "refs/heads/main"); got != sha2 {
		t.Fatalf("fast-forward target sha = %s, want %s", got, sha2)
	}

	rightWork := filepath.Join(root, "right-work")
	mustGit(t, "", "clone", "-b", "main", right, rightWork)
	mustGit(t, rightWork, "config", "user.email", "test@example.invalid")
	mustGit(t, rightWork, "config", "user.name", "gitmirror test")
	mustWrite(t, filepath.Join(rightWork, "right.txt"), "right\n")
	mustGit(t, rightWork, "add", "right.txt")
	mustGit(t, rightWork, "commit", "-m", "right diverges")
	mustGit(t, rightWork, "push", "origin", "main")
	rightSHA := gitOut(t, rightWork, "rev-parse", "HEAD")

	mustWrite(t, filepath.Join(work, "left.txt"), "left\n")
	mustGit(t, work, "add", "left.txt")
	mustGit(t, work, "commit", "-m", "left diverges")
	mustGit(t, work, "push", "origin", "main")
	leftSHA := gitOut(t, work, "rev-parse", "HEAD")
	if err := s.Process(ctx, pushEvent("acme/left", "refs/heads/main", sha2, leftSHA, false)); err != nil {
		t.Fatal(err)
	}
	if got := remoteRef(t, right, "refs/heads/main"); got != rightSHA {
		t.Fatalf("divergence changed target: got %s want %s", got, rightSHA)
	}
	if !remoteHasSHAUnder(t, right, "refs/heads/gitmirror/quarantine/", leftSHA) {
		t.Fatalf("divergent source %s was not preserved in a quarantine branch", leftSHA)
	}
}

func TestDeleteRequiresExpectedSHA(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	left := filepath.Join(root, "left.git")
	right := filepath.Join(root, "right.git")
	mustGit(t, "", "init", "--bare", left)
	mustGit(t, "", "init", "--bare", right)
	work := filepath.Join(root, "work")
	mustGit(t, "", "init", "-b", "main", work)
	mustGit(t, work, "config", "user.email", "test@example.invalid")
	mustGit(t, work, "config", "user.name", "gitmirror test")
	mustWrite(t, filepath.Join(work, "file.txt"), "one\n")
	mustGit(t, work, "add", "file.txt")
	mustGit(t, work, "commit", "-m", "one")
	mustGit(t, work, "remote", "add", "origin", left)
	mustGit(t, work, "push", "origin", "main")
	sha := gitOut(t, work, "rev-parse", "HEAD")
	s := New(testConfig(root, left, right))
	if err := s.Process(ctx, pushEvent("acme/left", "refs/heads/main", strings.Repeat("0", 40), sha, false)); err != nil {
		t.Fatal(err)
	}
	mustGit(t, work, "push", "origin", ":main")
	if err := s.Process(ctx, pushEvent("acme/left", "refs/heads/main", sha, strings.Repeat("0", 40), true)); err != nil {
		t.Fatal(err)
	}
	if got := remoteRef(t, right, "refs/heads/main"); got != "" {
		t.Fatalf("target branch still exists at %s", got)
	}
}

func TestHumanInLoopBlocksMainUntilExactApproval(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	left := filepath.Join(root, "left.git")
	right := filepath.Join(root, "right.git")
	mustGit(t, "", "init", "--bare", left)
	mustGit(t, "", "init", "--bare", right)
	work := filepath.Join(root, "work")
	mustGit(t, "", "init", "-b", "main", work)
	mustGit(t, work, "config", "user.email", "test@example.invalid")
	mustGit(t, work, "config", "user.name", "gitmirror test")
	mustWrite(t, filepath.Join(work, "file.txt"), "one\n")
	mustGit(t, work, "add", "file.txt")
	mustGit(t, work, "commit", "-m", "one")
	mustGit(t, work, "remote", "add", "origin", left)
	mustGit(t, work, "push", "origin", "main")
	sha := gitOut(t, work, "rev-parse", "HEAD")

	cfg := testConfig(root, left, right)
	cfg.Pairs[0].Right.HumanInLoopBranches = []string{"main"}
	s := New(cfg)
	if err := s.Process(ctx, pushEvent("acme/left", "refs/heads/main", strings.Repeat("0", 40), sha, false)); err != nil {
		t.Fatal(err)
	}
	if got := remoteRef(t, right, "refs/heads/main"); got != "" {
		t.Fatalf("HIL-protected main was pushed before approval: %s", got)
	}
	id := pendingApprovalID(t, cfg.DataDir)
	if err := s.Approve(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := remoteRef(t, right, "refs/heads/main"); got != sha {
		t.Fatalf("approved main sha = %s, want %s", got, sha)
	}
}

func TestHumanApprovalExpiresWhenTargetMoves(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	left := filepath.Join(root, "left.git")
	right := filepath.Join(root, "right.git")
	mustGit(t, "", "init", "--bare", left)
	mustGit(t, "", "init", "--bare", right)
	work := filepath.Join(root, "work")
	mustGit(t, "", "init", "-b", "main", work)
	mustGit(t, work, "config", "user.email", "test@example.invalid")
	mustGit(t, work, "config", "user.name", "gitmirror test")
	mustWrite(t, filepath.Join(work, "file.txt"), "one\n")
	mustGit(t, work, "add", "file.txt")
	mustGit(t, work, "commit", "-m", "one")
	mustGit(t, work, "remote", "add", "origin", left)
	mustGit(t, work, "push", "origin", "main")
	sha := gitOut(t, work, "rev-parse", "HEAD")

	cfg := testConfig(root, left, right)
	cfg.Pairs[0].Right.HumanInLoopBranches = []string{"main"}
	s := New(cfg)
	if err := s.Process(ctx, pushEvent("acme/left", "refs/heads/main", strings.Repeat("0", 40), sha, false)); err != nil {
		t.Fatal(err)
	}
	id := pendingApprovalID(t, cfg.DataDir)

	other := filepath.Join(root, "other")
	mustGit(t, "", "clone", left, other)
	mustGit(t, other, "checkout", "-b", "unrelated")
	mustGit(t, other, "config", "user.email", "test@example.invalid")
	mustGit(t, other, "config", "user.name", "gitmirror test")
	mustWrite(t, filepath.Join(other, "other.txt"), "other\n")
	mustGit(t, other, "add", "other.txt")
	mustGit(t, other, "commit", "-m", "other")
	otherSHA := gitOut(t, other, "rev-parse", "HEAD")
	mustGit(t, other, "push", right, "HEAD:refs/heads/main")

	if err := s.Approve(ctx, id); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired approval, got %v", err)
	}
	if got := remoteRef(t, right, "refs/heads/main"); got != otherSHA {
		t.Fatalf("expired approval modified target: got %s want %s", got, otherSHA)
	}
}

func testConfig(root, left, right string) config.Config {
	return config.Config{DataDir: filepath.Join(root, "data"), Pairs: []config.Pair{{Name: "test", Left: config.Repo{FullName: "acme/left", URL: left}, Right: config.Repo{FullName: "mirror/right", URL: right}}}}
}

func pushEvent(repo, ref, before, after string, deleted bool) queue.Event {
	body, _ := json.Marshal(map[string]any{"ref": ref, "before": before, "after": after, "deleted": deleted, "repository": map[string]any{"full_name": repo}})
	return queue.Event{Type: "push", Delivery: "test", Body: body}
}

func mustGit(t *testing.T, dir string, args ...string) { t.Helper(); _ = gitOut(t, dir, args...) }

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func remoteRef(t *testing.T, repo, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "ls-remote", repo, ref)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func remoteHasSHAUnder(t *testing.T, repo, prefix, wantSHA string) bool {
	t.Helper()
	cmd := exec.Command("git", "ls-remote", "--heads", repo, prefix+"*")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == wantSHA {
			return true
		}
	}
	return false
}

func pendingApprovalID(t *testing.T, dataDir string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "approvals", "pending"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("pending approvals = %d, want 1", len(entries))
	}
	return strings.TrimSuffix(entries[0].Name(), ".json")
}

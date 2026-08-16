package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapCreatesReadOnlyLocalBase(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	left := filepath.Join(root, "left.git")
	right := filepath.Join(root, "right.git")
	mustGit(t, "", "init", "--bare", left)
	mustGit(t, "", "init", "--bare", right)

	leftWork := filepath.Join(root, "left-work")
	mustGit(t, "", "init", "-b", "main", leftWork)
	mustGit(t, leftWork, "config", "user.email", "test@example.invalid")
	mustGit(t, leftWork, "config", "user.name", "gitmirror test")
	mustWrite(t, filepath.Join(leftWork, "left.txt"), "left\n")
	mustGit(t, leftWork, "add", "left.txt")
	mustGit(t, leftWork, "commit", "-m", "left")
	mustGit(t, leftWork, "remote", "add", "origin", left)
	mustGit(t, leftWork, "push", "origin", "main")
	leftSHA := gitOut(t, leftWork, "rev-parse", "HEAD")

	rightWork := filepath.Join(root, "right-work")
	mustGit(t, "", "init", "-b", "develop", rightWork)
	mustGit(t, rightWork, "config", "user.email", "test@example.invalid")
	mustGit(t, rightWork, "config", "user.name", "gitmirror test")
	mustWrite(t, filepath.Join(rightWork, "right.txt"), "right\n")
	mustGit(t, rightWork, "add", "right.txt")
	mustGit(t, rightWork, "commit", "-m", "right")
	mustGit(t, rightWork, "remote", "add", "origin", right)
	mustGit(t, rightWork, "push", "origin", "develop")
	rightSHA := gitOut(t, rightWork, "rev-parse", "HEAD")

	s := New(testConfig(root, left, right))
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}

	repoDir := filepath.Join(root, "repos", "test.git")
	if got := gitOut(t, repoDir, "rev-parse", "refs/gitmirror/bootstrap/left/main"); got != leftSHA {
		t.Fatalf("left bootstrap sha = %s, want %s", got, leftSHA)
	}
	if got := gitOut(t, repoDir, "rev-parse", "refs/gitmirror/bootstrap/right/develop"); got != rightSHA {
		t.Fatalf("right bootstrap sha = %s, want %s", got, rightSHA)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "gitmirror-bootstrap-v1")); err != nil {
		t.Fatalf("bootstrap marker: %v", err)
	}

	// Bootstrap must never mutate either remote.
	if got := remoteRef(t, left, "refs/heads/main"); got != leftSHA {
		t.Fatalf("left remote changed during bootstrap: %s", got)
	}
	if got := remoteRef(t, right, "refs/heads/develop"); got != rightSHA {
		t.Fatalf("right remote changed during bootstrap: %s", got)
	}
}

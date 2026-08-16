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

	// Deliberately create an unrelated main branch on the other side. Bootstrap
	// must preserve both histories exactly as-is instead of trying to reconcile,
	// fast-forward, delete, or otherwise choose a winner.
	rightWork := filepath.Join(root, "right-work")
	mustGit(t, "", "init", "-b", "main", rightWork)
	mustGit(t, rightWork, "config", "user.email", "test@example.invalid")
	mustGit(t, rightWork, "config", "user.name", "gitmirror test")
	mustWrite(t, filepath.Join(rightWork, "right.txt"), "right\n")
	mustGit(t, rightWork, "add", "right.txt")
	mustGit(t, rightWork, "commit", "-m", "right")
	mustGit(t, rightWork, "remote", "add", "origin", right)
	mustGit(t, rightWork, "push", "origin", "main")
	rightSHA := gitOut(t, rightWork, "rev-parse", "HEAD")
	if leftSHA == rightSHA {
		t.Fatal("test setup unexpectedly produced identical remote histories")
	}

	s := New(testConfig(root, left, right))
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}

	repoDir := filepath.Join(root, "repos", "test.git")
	if got := gitOut(t, repoDir, "rev-parse", "refs/gitmirror/bootstrap/left/main"); got != leftSHA {
		t.Fatalf("left bootstrap sha = %s, want %s", got, leftSHA)
	}
	if got := gitOut(t, repoDir, "rev-parse", "refs/gitmirror/bootstrap/right/main"); got != rightSHA {
		t.Fatalf("right bootstrap sha = %s, want %s", got, rightSHA)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "gitmirror-bootstrap-v1")); err != nil {
		t.Fatalf("bootstrap marker: %v", err)
	}

	// Bootstrap must never mutate either remote, even when both already contain
	// a branch with the same name and incompatible histories.
	if got := remoteRef(t, left, "refs/heads/main"); got != leftSHA {
		t.Fatalf("left remote changed during bootstrap: got %s want %s", got, leftSHA)
	}
	if got := remoteRef(t, right, "refs/heads/main"); got != rightSHA {
		t.Fatalf("right remote changed during bootstrap: got %s want %s", got, rightSHA)
	}

	// Re-running bootstrap must also remain a complete no-op toward both remotes.
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if got := remoteRef(t, left, "refs/heads/main"); got != leftSHA {
		t.Fatalf("left remote changed on repeated bootstrap: got %s want %s", got, leftSHA)
	}
	if got := remoteRef(t, right, "refs/heads/main"); got != rightSHA {
		t.Fatalf("right remote changed on repeated bootstrap: got %s want %s", got, rightSHA)
	}
}

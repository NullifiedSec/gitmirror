package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapSeedsCompletelyEmptyRightFromLeft(t *testing.T) {
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
	mustWrite(t, filepath.Join(work, "main.txt"), "main\n")
	mustGit(t, work, "add", "main.txt")
	mustGit(t, work, "commit", "-m", "main")
	mustGit(t, work, "branch", "develop")
	mustGit(t, work, "remote", "add", "origin", left)
	mustGit(t, work, "push", "origin", "main", "develop")
	mainSHA := gitOut(t, work, "rev-parse", "main")
	developSHA := gitOut(t, work, "rev-parse", "develop")

	cfg := testConfig(root, left, right)
	s := New(cfg)
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}

	if got := remoteRef(t, left, "refs/heads/main"); got != mainSHA {
		t.Fatalf("source main changed during bootstrap: got %s want %s", got, mainSHA)
	}
	if got := remoteRef(t, right, "refs/heads/main"); got != mainSHA {
		t.Fatalf("empty right main was not seeded: got %s want %s", got, mainSHA)
	}
	if got := remoteRef(t, right, "refs/heads/develop"); got != developSHA {
		t.Fatalf("empty right develop was not seeded: got %s want %s", got, developSHA)
	}

	repoDir := filepath.Join(cfg.DataDir, "repos", safePairName(cfg.Pairs[0].Name)+".git")
	if _, err := os.Stat(filepath.Join(repoDir, "gitmirror-bootstrap-v2")); err != nil {
		t.Fatalf("bootstrap marker: %v", err)
	}
}

func TestBootstrapSeedsCompletelyEmptyLeftFromRight(t *testing.T) {
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
	mustWrite(t, filepath.Join(work, "right.txt"), "right\n")
	mustGit(t, work, "add", "right.txt")
	mustGit(t, work, "commit", "-m", "right")
	mustGit(t, work, "remote", "add", "origin", right)
	mustGit(t, work, "push", "origin", "main")
	sha := gitOut(t, work, "rev-parse", "main")

	s := New(testConfig(root, left, right))
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if got := remoteRef(t, left, "refs/heads/main"); got != sha {
		t.Fatalf("empty left main was not seeded: got %s want %s", got, sha)
	}
	if got := remoteRef(t, right, "refs/heads/main"); got != sha {
		t.Fatalf("source right changed during bootstrap: got %s want %s", got, sha)
	}
}

func TestBootstrapPreservesBothWhenEitherAlreadyContainsRefs(t *testing.T) {
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
	// must preserve both histories exactly as-is instead of trying to reconcile.
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

	cfg := testConfig(root, left, right)
	s := New(cfg)
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}

	repoDir := filepath.Join(cfg.DataDir, "repos", safePairName(cfg.Pairs[0].Name)+".git")
	if got := gitOut(t, repoDir, "rev-parse", "refs/gitmirror/bootstrap/left/main"); got != leftSHA {
		t.Fatalf("left bootstrap sha = %s, want %s", got, leftSHA)
	}
	if got := gitOut(t, repoDir, "rev-parse", "refs/gitmirror/bootstrap/right/main"); got != rightSHA {
		t.Fatalf("right bootstrap sha = %s, want %s", got, rightSHA)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "gitmirror-bootstrap-v2")); err != nil {
		t.Fatalf("bootstrap marker: %v", err)
	}

	if got := remoteRef(t, left, "refs/heads/main"); got != leftSHA {
		t.Fatalf("left remote changed during bootstrap: got %s want %s", got, leftSHA)
	}
	if got := remoteRef(t, right, "refs/heads/main"); got != rightSHA {
		t.Fatalf("right remote changed during bootstrap: got %s want %s", got, rightSHA)
	}

	// Re-running bootstrap remains a no-op once v2 initialization completed.
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

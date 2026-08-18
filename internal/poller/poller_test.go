package poller

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

type recordingQueue struct {
	events []queue.Event
}

func (q *recordingQueue) Enqueue(e queue.Event) (bool, error) {
	q.events = append(q.events, e)
	return true, nil
}

func TestPollOnceReconcilesCounterpartAndDetectsDelete(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sourceRemote := filepath.Join(root, "source.git")
	targetRemote := filepath.Join(root, "target.git")
	work := filepath.Join(root, "work")
	mustGit(t, "", "init", "--bare", sourceRemote)
	mustGit(t, "", "init", "--bare", targetRemote)
	mustGit(t, "", "init", "-b", "main", work)
	mustGit(t, work, "config", "user.email", "test@example.invalid")
	mustGit(t, work, "config", "user.name", "gitmirror test")
	mustWrite(t, filepath.Join(work, "file.txt"), "one\n")
	mustGit(t, work, "add", "file.txt")
	mustGit(t, work, "commit", "-m", "one")
	mustGit(t, work, "remote", "add", "source", sourceRemote)
	mustGit(t, work, "remote", "add", "target", targetRemote)
	mustGit(t, work, "push", "source", "main")
	sha1 := gitOut(t, work, "rev-parse", "HEAD")

	cfg := config.Config{DataDir: filepath.Join(root, "data")}
	q := &recordingQueue{}
	r := New(cfg, q)
	source := config.Repo{Provider: config.ProviderGitHub, FullName: "acme/source", URL: sourceRemote, Polling: true, PollingFrequency: 120}
	target := config.Repo{Provider: config.ProviderGitHub, FullName: "mirror/target", URL: targetRemote}

	if err := r.pollOnce(ctx, source, target); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, q.events, 0, "refs/heads/main", zeroSHA, sha1, false)

	mustGit(t, work, "push", "target", "main")
	if err := r.pollOnce(ctx, source, target); err != nil {
		t.Fatal(err)
	}
	if len(q.events) != 1 {
		t.Fatalf("equal counterpart emitted %d events, want 1 total", len(q.events))
	}

	mustGit(t, work, "push", "target", ":main")
	if err := r.pollOnce(ctx, source, target); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, q.events, 1, "refs/heads/main", zeroSHA, sha1, false)
	mustGit(t, work, "push", "target", "main")

	mustWrite(t, filepath.Join(work, "file.txt"), "two\n")
	mustGit(t, work, "add", "file.txt")
	mustGit(t, work, "commit", "-m", "two")
	mustGit(t, work, "push", "source", "main")
	sha2 := gitOut(t, work, "rev-parse", "HEAD")
	if err := r.pollOnce(ctx, source, target); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, q.events, 2, "refs/heads/main", sha1, sha2, false)

	mustGit(t, work, "push", "target", "main")
	mustGit(t, work, "push", "source", ":main")
	if err := r.pollOnce(ctx, source, target); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, q.events, 3, "refs/heads/main", sha2, zeroSHA, true)
}

func TestReconcileAllIgnoresPollingFlags(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	left := filepath.Join(root, "left.git")
	right := filepath.Join(root, "right.git")
	work := filepath.Join(root, "work")
	mustGit(t, "", "init", "--bare", left)
	mustGit(t, "", "init", "--bare", right)
	mustGit(t, "", "init", "-b", "main", work)
	mustGit(t, work, "config", "user.email", "test@example.invalid")
	mustGit(t, work, "config", "user.name", "gitmirror test")
	mustWrite(t, filepath.Join(work, "file.txt"), "manual\n")
	mustGit(t, work, "add", "file.txt")
	mustGit(t, work, "commit", "-m", "manual")
	mustGit(t, work, "remote", "add", "left", left)
	mustGit(t, work, "push", "left", "main")
	sha := gitOut(t, work, "rev-parse", "HEAD")

	cfg := config.Config{DataDir: filepath.Join(root, "data"), Pairs: []config.Pair{{
		Name:  "manual",
		Left:  config.Repo{Provider: config.ProviderGitHub, FullName: "acme/left", URL: left, Polling: false},
		Right: config.Repo{Provider: config.ProviderGitHub, FullName: "mirror/right", URL: right, Polling: false},
	}}}
	q := &recordingQueue{}
	if err := New(cfg, q).ReconcileAll(ctx); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, q.events, 0, "refs/heads/main", zeroSHA, sha, false)
}

func assertEvent(t *testing.T, events []queue.Event, index int, ref, before, after string, deleted bool) {
	t.Helper()
	if len(events) <= index {
		t.Fatalf("missing event %d; got %d events", index, len(events))
	}
	var payload pushPayload
	if err := json.Unmarshal(events[index].Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Ref != ref || payload.Before != before || payload.After != after || payload.Deleted != deleted {
		t.Fatalf("event %d = %#v, want ref=%s before=%s after=%s deleted=%t", index, payload, ref, before, after, deleted)
	}
	if events[index].Provider != config.ProviderGitHub || events[index].Type != "push" || !strings.HasPrefix(events[index].Delivery, "poll-") {
		t.Fatalf("unexpected event envelope: %#v", events[index])
	}
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

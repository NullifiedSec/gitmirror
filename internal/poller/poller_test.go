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

func TestPollOnceDetectsCreateUpdateAndDelete(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	mustGit(t, "", "init", "--bare", remote)
	mustGit(t, "", "init", "-b", "main", work)
	mustGit(t, work, "config", "user.email", "test@example.invalid")
	mustGit(t, work, "config", "user.name", "gitmirror test")
	mustWrite(t, filepath.Join(work, "file.txt"), "one\n")
	mustGit(t, work, "add", "file.txt")
	mustGit(t, work, "commit", "-m", "one")
	mustGit(t, work, "remote", "add", "origin", remote)
	mustGit(t, work, "push", "origin", "main")
	sha1 := gitOut(t, work, "rev-parse", "HEAD")

	cfg := config.Config{DataDir: filepath.Join(root, "data")}
	q := &recordingQueue{}
	r := New(cfg, q)
	repo := config.Repo{Provider: config.ProviderGitHub, FullName: "acme/source", URL: remote, Polling: true, PollingFrequency: 120}

	if err := r.pollOnce(ctx, repo); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, q.events, 0, "refs/heads/main", zeroSHA, sha1, false)

	if err := r.pollOnce(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if len(q.events) != 1 {
		t.Fatalf("unchanged poll emitted %d events, want 1 total", len(q.events))
	}

	mustWrite(t, filepath.Join(work, "file.txt"), "two\n")
	mustGit(t, work, "add", "file.txt")
	mustGit(t, work, "commit", "-m", "two")
	mustGit(t, work, "push", "origin", "main")
	sha2 := gitOut(t, work, "rev-parse", "HEAD")
	if err := r.pollOnce(ctx, repo); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, q.events, 1, "refs/heads/main", sha1, sha2, false)

	mustGit(t, work, "push", "origin", ":main")
	if err := r.pollOnce(ctx, repo); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, q.events, 2, "refs/heads/main", sha2, zeroSHA, true)
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

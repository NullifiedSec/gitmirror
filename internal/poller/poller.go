package poller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NullifiedSec/gitmirror/internal/config"
	"github.com/NullifiedSec/gitmirror/internal/queue"
)

const zeroSHA = "0000000000000000000000000000000000000000"

type Enqueuer interface {
	Enqueue(queue.Event) (bool, error)
}

type Runner struct {
	cfg   config.Config
	queue Enqueuer
	now   func() time.Time
}

type snapshot struct {
	Refs map[string]string `json:"refs"`
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

func New(cfg config.Config, q Enqueuer) *Runner {
	return &Runner{cfg: cfg, queue: q, now: time.Now}
}

func (r *Runner) Run(ctx context.Context) {
	for _, pair := range r.cfg.Pairs {
		if pair.Left.Polling {
			go r.runRepo(ctx, pair.Left)
		}
		if pair.Right.Polling {
			go r.runRepo(ctx, pair.Right)
		}
	}
	<-ctx.Done()
}

func (r *Runner) runRepo(ctx context.Context, repo config.Repo) {
	poll := func() {
		if err := r.pollOnce(ctx, repo); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("poll %s/%s: %v", normalizeProvider(repo.Provider), repo.FullName, err)
		}
	}

	poll()
	frequency := repo.PollingFrequency
	if frequency <= 0 {
		frequency = config.DefaultPollingFrequency
	}
	ticker := time.NewTicker(time.Duration(frequency) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}

func (r *Runner) pollOnce(ctx context.Context, repo config.Repo) error {
	current, err := scanRemote(ctx, repo.URL)
	if err != nil {
		return err
	}
	previous, err := r.loadSnapshot(repo)
	if err != nil {
		return err
	}

	refs := make(map[string]struct{}, len(previous)+len(current))
	for ref := range previous {
		refs[ref] = struct{}{}
	}
	for ref := range current {
		refs[ref] = struct{}{}
	}
	ordered := make([]string, 0, len(refs))
	for ref := range refs {
		ordered = append(ordered, ref)
	}
	sort.Strings(ordered)

	for _, ref := range ordered {
		before, existedBefore := previous[ref]
		after, existsNow := current[ref]
		if existedBefore && existsNow && before == after {
			continue
		}
		if !existedBefore {
			before = zeroSHA
		}
		if !existsNow {
			after = zeroSHA
		}
		if err := r.enqueue(repo, ref, before, after, !existsNow); err != nil {
			return err
		}
	}

	return r.saveSnapshot(repo, current)
}

func scanRemote(ctx context.Context, url string) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", url)
	out, err := cmd.CombinedOutput()
	text := strings.ReplaceAll(string(out), url, "[REDACTED_REMOTE]")
	if err != nil {
		return nil, fmt.Errorf("scan remote refs: %w: %s", err, strings.TrimSpace(text))
	}
	refs := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "refs/heads/") {
			continue
		}
		refs[fields[1]] = fields[0]
	}
	return refs, nil
}

func (r *Runner) enqueue(repo config.Repo, ref, before, after string, deleted bool) error {
	payload := pushPayload{Ref: ref, Before: before, After: after, Deleted: deleted}
	payload.Repo.FullName = repo.FullName
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	e := queue.Event{
		Provider:   normalizeProvider(repo.Provider),
		Delivery:   deliveryID(repo, ref, before, after, deleted),
		Type:       "push",
		ReceivedAt: r.now().UTC(),
		Body:       body,
	}
	accepted, err := r.queue.Enqueue(e)
	if err != nil {
		return fmt.Errorf("enqueue polled ref %s: %w", ref, err)
	}
	if accepted {
		log.Printf("poll detected %s/%s %s %s -> %s", e.Provider, repo.FullName, ref, shortSHA(before), shortSHA(after))
	}
	return nil
}

func (r *Runner) loadSnapshot(repo config.Repo) (map[string]string, error) {
	path := r.snapshotPath(repo)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var state snapshot
	if err := json.Unmarshal(b, &state); err != nil {
		return nil, fmt.Errorf("decode polling snapshot: %w", err)
	}
	if state.Refs == nil {
		state.Refs = map[string]string{}
	}
	return state.Refs, nil
}

func (r *Runner) saveSnapshot(repo config.Repo, refs map[string]string) error {
	dir := filepath.Join(r.cfg.DataDir, "poll")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(snapshot{Refs: refs})
	if err != nil {
		return err
	}
	path := r.snapshotPath(repo)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (r *Runner) snapshotPath(repo config.Repo) string {
	sum := sha256.Sum256([]byte(normalizeProvider(repo.Provider) + ":" + strings.ToLower(repo.FullName)))
	return filepath.Join(r.cfg.DataDir, "poll", fmt.Sprintf("%x.json", sum[:12]))
}

func deliveryID(repo config.Repo, ref, before, after string, deleted bool) string {
	value := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%t", normalizeProvider(repo.Provider), strings.ToLower(repo.FullName), ref, before, after, deleted)
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("poll-%x", sum[:16])
}

func normalizeProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return config.ProviderGitHub
	}
	return provider
}

func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

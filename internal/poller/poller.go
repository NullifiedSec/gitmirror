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
			go r.runRepo(ctx, pair.Left, pair.Right)
		}
		if pair.Right.Polling {
			go r.runRepo(ctx, pair.Right, pair.Left)
		}
	}
	<-ctx.Done()
}

// ReconcileAll performs one full bidirectional reconciliation pass over every
// configured pair, regardless of whether polling is enabled for either side.
// It deliberately reuses the same comparison/snapshot/event path as polling so
// manual recovery from missed webhooks cannot bypass normal sync safety rules.
func (r *Runner) ReconcileAll(ctx context.Context) error {
	for _, pair := range r.cfg.Pairs {
		if err := r.pollOnce(ctx, pair.Left, pair.Right); err != nil {
			return fmt.Errorf("reconcile %s left->right: %w", pair.Name, err)
		}
		if err := r.pollOnce(ctx, pair.Right, pair.Left); err != nil {
			return fmt.Errorf("reconcile %s right->left: %w", pair.Name, err)
		}
	}
	return nil
}

func (r *Runner) runRepo(ctx context.Context, source, target config.Repo) {
	poll := func() {
		if err := r.pollOnce(ctx, source, target); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("poll %s/%s: %v", normalizeProvider(source.Provider), source.FullName, err)
		}
	}

	poll()
	frequency := source.PollingFrequency
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

func (r *Runner) pollOnce(ctx context.Context, source, target config.Repo) error {
	current, err := scanRemote(ctx, source.URL)
	if err != nil {
		return fmt.Errorf("scan source %s: %w", source.FullName, err)
	}
	targetRefs, err := scanRemote(ctx, target.URL)
	if err != nil {
		return fmt.Errorf("scan target %s: %w", target.FullName, err)
	}
	previous, err := r.loadSnapshot(source)
	if err != nil {
		return err
	}

	ordered := make([]string, 0, len(current))
	for ref := range current {
		ordered = append(ordered, ref)
	}
	sort.Strings(ordered)

	for _, ref := range ordered {
		sourceSHA := current[ref]
		targetSHA, targetExists := targetRefs[ref]
		if targetExists && targetSHA == sourceSHA {
			continue
		}
		before := targetSHA
		if !targetExists {
			before = zeroSHA
		}
		if err := r.enqueue(source, ref, before, sourceSHA, false); err != nil {
			return err
		}
	}

	deletedRefs := make([]string, 0)
	for ref, previousSHA := range previous {
		if _, stillExists := current[ref]; stillExists {
			continue
		}
		if targetSHA, targetExists := targetRefs[ref]; targetExists && targetSHA == previousSHA {
			deletedRefs = append(deletedRefs, ref)
		}
	}
	sort.Strings(deletedRefs)
	for _, ref := range deletedRefs {
		if err := r.enqueue(source, ref, previous[ref], zeroSHA, true); err != nil {
			return err
		}
	}

	return r.saveSnapshot(source, current)
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
		log.Printf("poll reconcile %s/%s %s %s -> %s", e.Provider, repo.FullName, ref, shortSHA(before), shortSHA(after))
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

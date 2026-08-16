package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxAttempts = 5

type Event struct {
	Delivery   string          `json:"delivery"`
	Type       string          `json:"type"`
	ReceivedAt time.Time       `json:"received_at"`
	Attempts   int             `json:"attempts"`
	Body       json.RawMessage `json:"body"`
}

type Queue struct {
	root string
	mu   sync.Mutex
}

func New(root string) *Queue { return &Queue{root: root} }

func (q *Queue) Init() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, dir := range []string{"pending", "processing", "done", "failed"} {
		if err := os.MkdirAll(filepath.Join(q.root, "queue", dir), 0o700); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(q.dir("processing"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		from := filepath.Join(q.dir("processing"), entry.Name())
		to := filepath.Join(q.dir("pending"), entry.Name())
		if err := os.Rename(from, to); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (q *Queue) Enqueue(e Event) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if e.Delivery == "" {
		return false, fmt.Errorf("delivery id is required")
	}
	name := safeName(e.Delivery) + ".json"
	for _, state := range []string{"pending", "processing", "done"} {
		if _, err := os.Stat(filepath.Join(q.dir(state), name)); err == nil {
			return false, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	_ = os.Remove(filepath.Join(q.dir("failed"), name))
	b, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	path := filepath.Join(q.dir("pending"), name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return false, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	return true, nil
}

func (q *Queue) Claim() (Event, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries, err := os.ReadDir(q.dir("pending"))
	if err != nil {
		return Event{}, false, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return Event{}, false, nil
	}
	sort.Strings(names)
	name := names[0]
	from := filepath.Join(q.dir("pending"), name)
	to := filepath.Join(q.dir("processing"), name)
	if err := os.Rename(from, to); err != nil {
		return Event{}, false, err
	}
	b, err := os.ReadFile(to)
	if err != nil {
		return Event{}, false, err
	}
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return Event{}, false, err
	}
	e.Attempts++
	if err := q.write(to, e); err != nil {
		return Event{}, false, err
	}
	return e, true, nil
}

func (q *Queue) Complete(delivery string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	name := safeName(delivery) + ".json"
	return os.Rename(filepath.Join(q.dir("processing"), name), filepath.Join(q.dir("done"), name))
}

func (q *Queue) Fail(e Event) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	name := safeName(e.Delivery) + ".json"
	from := filepath.Join(q.dir("processing"), name)
	state := "pending"
	if e.Attempts >= maxAttempts {
		state = "failed"
	}
	return os.Rename(from, filepath.Join(q.dir(state), name))
}

func (q *Queue) dir(state string) string { return filepath.Join(q.root, "queue", state) }

func (q *Queue) write(path string, e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "event"
	}
	return b.String()
}

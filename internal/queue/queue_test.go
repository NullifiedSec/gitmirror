package queue

import (
	"encoding/json"
	"testing"
	"time"
)

func TestQueueLifecycleAndIdempotency(t *testing.T) {
	q := New(t.TempDir())
	if err := q.Init(); err != nil {
		t.Fatal(err)
	}
	e := Event{Provider: "github", Delivery: "abc-123", Type: "push", ReceivedAt: time.Now().UTC(), Body: json.RawMessage(`{"ok":true}`)}
	accepted, err := q.Enqueue(e)
	if err != nil || !accepted {
		t.Fatalf("first enqueue accepted=%v err=%v", accepted, err)
	}
	accepted, err = q.Enqueue(e)
	if err != nil || accepted {
		t.Fatalf("duplicate enqueue accepted=%v err=%v", accepted, err)
	}
	claimed, ok, err := q.Claim()
	if err != nil || !ok || claimed.Attempts != 1 {
		t.Fatalf("claim ok=%v attempts=%d err=%v", ok, claimed.Attempts, err)
	}
	if err := q.Fail(claimed); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err = q.Claim()
	if err != nil || !ok || claimed.Attempts != 2 {
		t.Fatalf("retry claim ok=%v attempts=%d err=%v", ok, claimed.Attempts, err)
	}
	if err := q.Complete(claimed); err != nil {
		t.Fatal(err)
	}
	accepted, err = q.Enqueue(e)
	if err != nil || accepted {
		t.Fatalf("completed delivery should remain idempotent: accepted=%v err=%v", accepted, err)
	}
}

func TestDeliveryIDsAreScopedByProvider(t *testing.T) {
	q := New(t.TempDir())
	if err := q.Init(); err != nil {
		t.Fatal(err)
	}
	github := Event{Provider: "github", Delivery: "same-id", Type: "push", Body: json.RawMessage(`{}`)}
	gitea := Event{Provider: "gitea", Delivery: "same-id", Type: "push", Body: json.RawMessage(`{}`)}
	if ok, err := q.Enqueue(github); err != nil || !ok {
		t.Fatalf("github enqueue accepted=%v err=%v", ok, err)
	}
	if ok, err := q.Enqueue(gitea); err != nil || !ok {
		t.Fatalf("gitea enqueue accepted=%v err=%v", ok, err)
	}
}

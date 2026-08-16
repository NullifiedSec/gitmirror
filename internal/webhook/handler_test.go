package webhook

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NullifiedSec/gitmirror/internal/queue"
)

type fakeQueue struct {
	events []queue.Event
}

func (f *fakeQueue) Enqueue(e queue.Event) (bool, error) {
	f.events = append(f.events, e)
	return true, nil
}

func TestVerify(t *testing.T) {
	secret := []byte("correct horse battery staple")
	body := []byte(`{"zen":"keep it logically awesome"}`)
	sig := SignForTest(secret, body)
	if !Verify(secret, body, sig) {
		t.Fatal("valid signature rejected")
	}
	if Verify(secret, []byte("tampered"), sig) {
		t.Fatal("tampered body accepted")
	}
	if Verify(nil, body, sig) {
		t.Fatal("empty secret accepted")
	}
}

func TestHandlerRejectsInvalidSignature(t *testing.T) {
	q := &fakeQueue{}
	h := New(ProviderGitHub, "secret", q)
	r := httptest.NewRequest(http.MethodPost, "/webhooks/github", http.NoBody)
	r.Header.Set("X-GitHub-Delivery", "delivery-1")
	r.Header.Set("X-GitHub-Event", "push")
	r.Header.Set("X-Hub-Signature-256", "sha256=00")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
	if len(q.events) != 0 {
		t.Fatal("invalid request was enqueued")
	}
}

func TestGitHubHandlerEnqueuesValidatedDelivery(t *testing.T) {
	q := &fakeQueue{}
	h := New(ProviderGitHub, "secret", q)
	body := []byte(`{"ref":"refs/heads/main"}`)
	r := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	r.Header.Set("X-Hub-Signature-256", SignForTest([]byte("secret"), body))
	r.Header.Set("X-GitHub-Delivery", "delivery-1")
	r.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	if len(q.events) != 1 || q.events[0].Provider != ProviderGitHub || q.events[0].Delivery != "delivery-1" || q.events[0].Type != "push" {
		t.Fatalf("unexpected queued event: %#v", q.events)
	}
}

func TestGiteaHandlerEnqueuesValidatedDelivery(t *testing.T) {
	q := &fakeQueue{}
	h := New(ProviderGitea, "secret", q)
	body := []byte(`{"ref":"refs/heads/main"}`)
	r := httptest.NewRequest(http.MethodPost, "/webhooks/gitea", bytes.NewReader(body))
	r.Header.Set("X-Hub-Signature-256", SignForTest([]byte("secret"), body))
	r.Header.Set("X-Gitea-Delivery", "delivery-1")
	r.Header.Set("X-Gitea-Event", "push")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	if len(q.events) != 1 || q.events[0].Provider != ProviderGitea || q.events[0].Delivery != "delivery-1" || q.events[0].Type != "push" {
		t.Fatalf("unexpected queued event: %#v", q.events)
	}
}

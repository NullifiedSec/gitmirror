package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NullifiedSec/gitmirror/internal/config"
	"github.com/NullifiedSec/gitmirror/internal/queue"
	"github.com/NullifiedSec/gitmirror/internal/syncer"
	"github.com/NullifiedSec/gitmirror/internal/webhook"
)

func main() {
	configPath := flag.String("config", "gitmirror.json", "path to configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	githubSecret := os.Getenv("GITMIRROR_GITHUB_WEBHOOK_SECRET")
	if githubSecret == "" {
		githubSecret = os.Getenv("GITMIRROR_WEBHOOK_SECRET")
	}
	giteaSecret := os.Getenv("GITMIRROR_GITEA_WEBHOOK_SECRET")
	if cfg.UsesProvider(config.ProviderGitHub) && githubSecret == "" {
		log.Fatal("GITMIRROR_GITHUB_WEBHOOK_SECRET (or legacy GITMIRROR_WEBHOOK_SECRET) is required")
	}
	if cfg.UsesProvider(config.ProviderGitea) && giteaSecret == "" {
		log.Fatal("GITMIRROR_GITEA_WEBHOOK_SECRET is required")
	}

	q := queue.New(cfg.DataDir)
	if err := q.Init(); err != nil {
		log.Fatalf("initialize queue: %v", err)
	}
	processor := syncer.New(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go worker(ctx, q, processor)

	mux := http.NewServeMux()
	if githubSecret != "" {
		mux.Handle("POST /webhooks/github", webhook.New(webhook.ProviderGitHub, githubSecret, q))
	}
	if giteaSecret != "" {
		mux.Handle("POST /webhooks/gitea", webhook.New(webhook.ProviderGitea, giteaSecret, q))
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		log.Printf("gitmirror listening on %s", cfg.Listen)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http server: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
}

type processor interface {
	Process(context.Context, queue.Event) error
}

func worker(ctx context.Context, q *queue.Queue, p processor) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e, ok, err := q.Claim()
			if err != nil {
				log.Printf("queue claim: %v", err)
				continue
			}
			if !ok {
				continue
			}
			if err := p.Process(ctx, e); err != nil {
				log.Printf("event %s/%s (%s) attempt %d failed: %v", e.Provider, e.Delivery, e.Type, e.Attempts, err)
				if failErr := q.Fail(e); failErr != nil {
					log.Printf("queue failure transition for %s: %v", e.Delivery, failErr)
				}
				continue
			}
			if err := q.Complete(e); err != nil {
				log.Printf("queue completion for %s/%s: %v", e.Provider, e.Delivery, err)
			}
		}
	}
}

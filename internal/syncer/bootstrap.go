package syncer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// Bootstrap ensures every configured pair has a local bare repository cache.
// A pair is fetched only when its bootstrap marker is absent. Bootstrap is
// deliberately read-only with respect to both remotes: branch tips are fetched
// into private refs/gitmirror/bootstrap/{left,right}/ namespaces and no push is
// ever performed from this path.
func (s *Syncer) Bootstrap(ctx context.Context) error {
	for _, pair := range s.cfg.Pairs {
		if err := s.bootstrapPair(ctx, pair); err != nil {
			return fmt.Errorf("bootstrap pair %s: %w", pair.Name, err)
		}
	}
	return nil
}

func (s *Syncer) bootstrapPair(ctx context.Context, pair config.Pair) error {
	repoDir := filepath.Join(s.dataDir, "repos", safePairName(pair.Name)+".git")
	marker := filepath.Join(repoDir, "gitmirror-bootstrap-v1")
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := s.ensureBare(ctx, repoDir, pair); err != nil {
		return err
	}

	for _, remote := range []string{"left", "right"} {
		refspec := "+refs/heads/*:refs/gitmirror/bootstrap/" + remote + "/*"
		if _, err := run(ctx, repoDir, s.secrets, "git", "fetch", "--no-tags", "--prune", remote, refspec); err != nil {
			return fmt.Errorf("fetch %s baseline: %w", remote, err)
		}
	}

	if err := os.WriteFile(marker, []byte("bootstrap-v1\n"), 0o600); err != nil {
		return fmt.Errorf("write bootstrap marker: %w", err)
	}
	log.Printf("bootstrapped local base for pair %s", pair.Name)
	return nil
}

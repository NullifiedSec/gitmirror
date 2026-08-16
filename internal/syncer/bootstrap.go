package syncer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NullifiedSec/gitmirror/internal/config"
)

// Bootstrap establishes the initial state for every configured pair before
// workers, polling, or webhook processing begin.
//
// Both remotes are always fetched into isolated local bootstrap namespaces. If
// exactly one remote is truly empty (it advertises no refs at all), the populated
// side is used to seed all of its branches into the empty side. Existing remote
// refs are never updated by this path: seeding uses creation-only leases and an
// atomic push, so any concurrent creation on the destination aborts the push.
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
	marker := filepath.Join(repoDir, "gitmirror-bootstrap-v2")
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := s.ensureBare(ctx, repoDir, pair); err != nil {
		return err
	}

	leftEmpty, err := s.remoteEmpty(ctx, repoDir, "left")
	if err != nil {
		return fmt.Errorf("inspect left remote: %w", err)
	}
	rightEmpty, err := s.remoteEmpty(ctx, repoDir, "right")
	if err != nil {
		return fmt.Errorf("inspect right remote: %w", err)
	}

	for _, remote := range []string{"left", "right"} {
		refspec := "+refs/heads/*:refs/gitmirror/bootstrap/" + remote + "/*"
		if _, err := run(ctx, repoDir, s.secrets, "git", "fetch", "--no-tags", "--prune", remote, refspec); err != nil {
			return fmt.Errorf("fetch %s baseline: %w", remote, err)
		}
	}

	switch {
	case leftEmpty && !rightEmpty:
		if err := s.seedEmptyRemote(ctx, repoDir, "right", "left"); err != nil {
			return fmt.Errorf("seed empty left remote: %w", err)
		}
		log.Printf("initially populated empty left side for pair %s from right", pair.Name)
	case rightEmpty && !leftEmpty:
		if err := s.seedEmptyRemote(ctx, repoDir, "left", "right"); err != nil {
			return fmt.Errorf("seed empty right remote: %w", err)
		}
		log.Printf("initially populated empty right side for pair %s from left", pair.Name)
	case !leftEmpty && !rightEmpty:
		log.Printf("bootstrap pair %s: both remotes already contain refs; preserving both without initial push", pair.Name)
	default:
		log.Printf("bootstrap pair %s: both remotes are empty; nothing to seed", pair.Name)
	}

	if err := os.WriteFile(marker, []byte("bootstrap-v2\n"), 0o600); err != nil {
		return fmt.Errorf("write bootstrap marker: %w", err)
	}
	log.Printf("bootstrapped local base for pair %s", pair.Name)
	return nil
}

func (s *Syncer) remoteEmpty(ctx context.Context, repoDir, remote string) (bool, error) {
	stdout, stderr, err := runSplit(ctx, repoDir, s.secrets, "git", "ls-remote", remote)
	if err != nil {
		return false, fmt.Errorf("ls-remote %s: %w", remote, err)
	}
	if strings.TrimSpace(stderr) != "" {
		return false, fmt.Errorf("ls-remote %s produced stderr", remote)
	}
	return strings.TrimSpace(stdout) == "", nil
}

// seedEmptyRemote copies every branch from the source bootstrap namespace into
// a destination that was observed as fully empty. It is intentionally stricter
// than normal synchronization:
//   - destination is rechecked and must still advertise zero refs;
//   - all branch creations are sent atomically;
//   - every branch has a force-with-lease expectation of "must not exist";
//   - no existing destination ref can be fast-forwarded, overwritten, or deleted.
//
// The lease form is used only as a compare-and-create guard; there is no force
// refspec and it cannot authorize replacing an existing ref.
func (s *Syncer) seedEmptyRemote(ctx context.Context, repoDir, source, target string) error {
	empty, err := s.remoteEmpty(ctx, repoDir, target)
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("refusing initial population: destination %s is no longer empty", target)
	}

	prefix := "refs/gitmirror/bootstrap/" + source + "/"
	out, err := run(ctx, repoDir, s.secrets, "git", "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return fmt.Errorf("list source bootstrap refs: %w", err)
	}

	var localRefs []string
	for _, line := range strings.Split(out, "\n") {
		ref := strings.TrimSpace(line)
		if ref != "" && strings.HasPrefix(ref, prefix) {
			localRefs = append(localRefs, ref)
		}
	}
	sort.Strings(localRefs)
	if len(localRefs) == 0 {
		return nil
	}

	args := []string{"push", "--atomic", "--porcelain"}
	for _, localRef := range localRefs {
		branch := strings.TrimPrefix(localRef, prefix)
		destRef := "refs/heads/" + branch
		// Empty expected value means the remote ref must not exist.
		args = append(args, "--force-with-lease="+destRef+":")
	}
	args = append(args, target)
	for _, localRef := range localRefs {
		branch := strings.TrimPrefix(localRef, prefix)
		args = append(args, localRef+":refs/heads/"+branch)
	}

	stdout, stderr, err := runSplit(ctx, repoDir, s.secrets, "git", args...)
	if err != nil {
		return fmt.Errorf("atomic creation-only seed push failed: %w", err)
	}
	if strings.TrimSpace(stderr) != "" {
		return fmt.Errorf("atomic seed push produced stderr; refusing to treat bootstrap as successful")
	}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "To ") || strings.HasPrefix(line, "Done") {
			continue
		}
		if !strings.HasPrefix(line, "*") && !strings.Contains(line, "[new branch]") {
			return fmt.Errorf("unexpected seed push status: %s", line)
		}
	}
	return nil
}

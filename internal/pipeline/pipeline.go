// Package pipeline runs discover, clone and scan for many repos in parallel,
// reusing cached facts for repos whose HEAD has not changed.
package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/rossedman/tfmetrics/internal/cache"
	"github.com/rossedman/tfmetrics/internal/discover"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/scan"
)

// Cloner fetches a repo; clone.Git satisfies it.
type Cloner interface {
	RemoteHead(ctx context.Context, url, ref string) (string, error)
	Clone(ctx context.Context, url, ref, dir string) (string, error)
}

// Options configure a run.
type Options struct {
	OutDir      string
	Concurrency int
	Ref         string // branch, tag or SHA; default branch when empty
	Cache       cache.Cache
	Cloner      Cloner
	Scan        scan.Options
	// Settings is folded into cache keys so a change to scan settings rescans.
	Settings string
	// Progress is called after each repo; may be nil.
	Progress func(repo string, cached bool, err error)
}

// Summary reports what a run did.
type Summary struct {
	Scanned int
	Cached  int
	Failed  map[string]error
}

// Run scans every repo and writes one facts file per repo to OutDir.
func Run(ctx context.Context, repos []discover.Repo, opts Options) Summary {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 1
	}
	sum := Summary{Failed: map[string]error{}}
	var mu sync.Mutex
	jobs := make(chan discover.Repo)
	var wg sync.WaitGroup
	for range opts.Concurrency {
		wg.Go(func() {
			for r := range jobs {
				cached, err := one(ctx, r, opts)
				mu.Lock()
				switch {
				case err != nil:
					sum.Failed[r.FullName] = err
				case cached:
					sum.Cached++
				default:
					sum.Scanned++
				}
				mu.Unlock()
				if opts.Progress != nil {
					opts.Progress(r.FullName, cached, err)
				}
			}
		})
	}
	for _, r := range repos {
		select {
		case jobs <- r:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	return sum
}

// one scans a single repo and reports whether cached facts were reused.
func one(ctx context.Context, r discover.Repo, opts Options) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	meta := scan.Meta{Repo: r.FullName, Org: r.Org, Topics: r.Topics, Properties: r.Properties}

	var commit string
	if r.LocalPath != "" {
		commit = scan.HeadCommit(ctx, r.LocalPath)
	} else {
		if opts.Cloner == nil {
			return false, fmt.Errorf("no cloner configured")
		}
		sha, err := opts.Cloner.RemoteHead(ctx, r.CloneURL, opts.Ref)
		if err != nil {
			return false, err
		}
		commit = sha
	}
	key := ""
	if commit != "" {
		key = cache.Key(commit, r.Topics, r.Properties, opts.Settings)
		if res, ok := opts.Cache.Get(r.FullName, key); ok {
			_, err := facts.Write(opts.OutDir, res)
			return true, err
		}
	}

	dir := r.LocalPath
	if dir == "" {
		tmp, err := os.MkdirTemp("", "tfmetrics-"+strings.ReplaceAll(r.Name, "/", "_")+"-")
		if err != nil {
			return false, err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		got, err := opts.Cloner.Clone(ctx, r.CloneURL, opts.Ref, tmp)
		if err != nil {
			return false, err
		}
		dir = tmp
		if got != commit {
			// HEAD moved between ls-remote and clone; key on what was scanned.
			commit = got
			key = cache.Key(commit, r.Topics, r.Properties, opts.Settings)
		}
	}
	meta.Commit = commit
	res, err := scan.Dir(ctx, dir, meta, opts.Scan)
	if err != nil {
		return false, err
	}
	if err := opts.Cache.Put(key, res); err != nil {
		return false, err
	}
	_, err = facts.Write(opts.OutDir, res)
	return false, err
}

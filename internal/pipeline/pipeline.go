// Package pipeline runs discover, clone and scan for many repos in parallel,
// reusing cached facts for repos whose HEAD has not changed.
package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

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

// Summary reports what a run did. Durations are summed across workers, so
// they show where the time goes rather than the wall-clock time.
type Summary struct {
	Scanned     int
	Cached      int
	Failed      map[string]error
	RemoteHeads int           // git ls-remote calls (repos without a discovered HEAD)
	Clones      int           // repos cloned
	HeadTime    time.Duration // time in ls-remote
	CloneTime   time.Duration // time cloning
	ScanTime    time.Duration // time classifying and parsing
	Wall        time.Duration // wall-clock time of the run
}

// work is what one repo cost.
type work struct {
	cached                     bool
	heads, clones              int
	headTime, cloneTime, parse time.Duration
}

// Run scans every repo and writes one facts file per repo to OutDir.
func Run(ctx context.Context, repos []discover.Repo, opts Options) Summary {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 1
	}
	start := time.Now()
	sum := Summary{Failed: map[string]error{}}
	var mu sync.Mutex
	jobs := make(chan discover.Repo)
	var wg sync.WaitGroup
	for range opts.Concurrency {
		wg.Go(func() {
			for r := range jobs {
				w, err := one(ctx, r, opts)
				cached := w.cached
				mu.Lock()
				sum.RemoteHeads += w.heads
				sum.Clones += w.clones
				sum.HeadTime += w.headTime
				sum.CloneTime += w.cloneTime
				sum.ScanTime += w.parse
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
	sum.Wall = time.Since(start)
	return sum
}

// one scans a single repo and reports what it cost and whether cached facts
// were reused.
func one(ctx context.Context, r discover.Repo, opts Options) (work, error) {
	var w work
	if err := ctx.Err(); err != nil {
		return w, err
	}
	meta := scan.Meta{Repo: r.FullName, Org: r.Org, Topics: r.Topics, Properties: r.Properties}

	var commit string
	switch {
	case r.LocalPath != "":
		commit = scan.HeadCommit(ctx, r.LocalPath)
	case opts.Cloner == nil:
		return w, fmt.Errorf("no cloner configured")
	case r.HeadSHA != "" && opts.Ref == "":
		commit = r.HeadSHA // from discovery: no git round trip needed
	default:
		t := time.Now()
		sha, err := opts.Cloner.RemoteHead(ctx, r.CloneURL, opts.Ref)
		w.heads, w.headTime = 1, time.Since(t)
		if err != nil {
			return w, err
		}
		commit = sha
	}
	key := ""
	if commit != "" {
		key = cache.Key(commit, r.Topics, r.Properties, opts.Settings)
		if res, ok := opts.Cache.Get(r.FullName, key); ok {
			w.cached = true
			_, err := facts.Write(opts.OutDir, res)
			return w, err
		}
	}

	dir := r.LocalPath
	if dir == "" {
		tmp, err := os.MkdirTemp("", "tfmetrics-"+strings.ReplaceAll(r.Name, "/", "_")+"-")
		if err != nil {
			return w, err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		t := time.Now()
		got, err := opts.Cloner.Clone(ctx, r.CloneURL, opts.Ref, tmp)
		w.clones, w.cloneTime = 1, time.Since(t)
		if err != nil {
			return w, err
		}
		dir = tmp
		if got != commit {
			// HEAD moved since it was looked up; key on what was scanned.
			commit = got
			key = cache.Key(commit, r.Topics, r.Properties, opts.Settings)
		}
	}
	meta.Commit = commit
	t := time.Now()
	res, err := scan.Dir(ctx, dir, meta, opts.Scan)
	w.parse = time.Since(t)
	if err != nil {
		return w, err
	}
	if err := opts.Cache.Put(key, res); err != nil {
		return w, err
	}
	_, err = facts.Write(opts.OutDir, res)
	return w, err
}

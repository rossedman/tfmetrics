package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rossedman/tfmetrics/internal/cache"
	"github.com/rossedman/tfmetrics/internal/discover"
	"github.com/rossedman/tfmetrics/internal/facts"
)

const sha1 = "1111111111111111111111111111111111111111"
const sha2 = "2222222222222222222222222222222222222222"

// fakeCloner is a Cloner that writes a tiny module into the clone dir.
type fakeCloner struct {
	head      func(url string) (string, error)
	cloneSHA  func(url string) string
	cloneErr  error
	heads     atomic.Int32
	clones    atomic.Int32
	inflight  atomic.Int32
	maxFlight atomic.Int32
	delay     time.Duration
	gotRef    atomic.Value
	cloneDirs sync.Map
}

func (f *fakeCloner) RemoteHead(_ context.Context, url, ref string) (string, error) {
	f.heads.Add(1)
	f.gotRef.Store(ref)
	if f.head != nil {
		return f.head(url)
	}
	return sha1, nil
}

func (f *fakeCloner) Clone(ctx context.Context, url, ref, dir string) (string, error) {
	f.clones.Add(1)
	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		m := f.maxFlight.Load()
		if n <= m || f.maxFlight.CompareAndSwap(m, n) {
			break
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if f.cloneErr != nil {
		return "", f.cloneErr
	}
	f.cloneDirs.Store(url, dir)
	body := "variable \"a\" {}\nresource \"null_resource\" \"x\" {}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(body), 0o644); err != nil {
		return "", err
	}
	if f.cloneSHA != nil {
		return f.cloneSHA(url), nil
	}
	return sha1, nil
}

func remoteRepo(name string) discover.Repo {
	return discover.Repo{
		FullName: "acme/" + name, Org: "acme", Name: name,
		CloneURL: "https://example.invalid/acme/" + name + ".git",
		Topics:   []string{"terraform"}, Properties: map[string]string{"team": "x"},
	}
}

func readFacts(t *testing.T, dir, repo string) facts.ScanResult {
	t.Helper()
	r, err := facts.Read(filepath.Join(dir, facts.FileName(repo)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunClones(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	fc := &fakeCloner{}
	sum := Run(context.Background(), []discover.Repo{remoteRepo("a"), remoteRepo("b")}, Options{
		OutDir: out, Cloner: fc, Cache: cache.Cache{Dir: t.TempDir()}, Ref: "release",
	})
	if sum.Scanned != 2 || sum.Cached != 0 || len(sum.Failed) != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	if fc.gotRef.Load() != "release" {
		t.Errorf("ref = %v", fc.gotRef.Load())
	}
	r := readFacts(t, out, "acme/a")
	if r.Commit != sha1 || r.Repo != "acme/a" || r.Org != "acme" || len(r.Modules) != 1 {
		t.Errorf("facts = %+v", r)
	}
	if r.Properties["team"] != "x" || len(r.Topics) != 1 {
		t.Errorf("metadata not carried: %+v", r)
	}
	// Temp clone dirs are removed.
	fc.cloneDirs.Range(func(_, d any) bool {
		if _, err := os.Stat(d.(string)); !os.IsNotExist(err) {
			t.Errorf("clone dir %v not cleaned up", d)
		}
		return true
	})
}

func TestRunCacheHit(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	repos := []discover.Repo{remoteRepo("a")}
	fc := &fakeCloner{}
	opts := Options{OutDir: t.TempDir(), Cloner: fc, Cache: cache.Cache{Dir: cacheDir}, Settings: "s1"}

	if sum := Run(context.Background(), repos, opts); sum.Scanned != 1 {
		t.Fatalf("first run = %+v", sum)
	}
	if fc.clones.Load() != 1 {
		t.Fatalf("clones = %d", fc.clones.Load())
	}

	// Second run, new OutDir: facts come from the cache without cloning.
	opts.OutDir = t.TempDir()
	var cachedSeen bool
	opts.Progress = func(_ string, cached bool, _ error) { cachedSeen = cached }
	sum := Run(context.Background(), repos, opts)
	if sum.Cached != 1 || sum.Scanned != 0 || len(sum.Failed) != 0 {
		t.Fatalf("second run = %+v", sum)
	}
	if fc.clones.Load() != 1 {
		t.Errorf("clones = %d, cache hit must not clone", fc.clones.Load())
	}
	if !cachedSeen {
		t.Error("progress did not report cached")
	}
	if r := readFacts(t, opts.OutDir, "acme/a"); r.Commit != sha1 {
		t.Errorf("cached facts not written: %+v", r)
	}

	// Changed settings, topics or properties invalidate the key.
	opts.Settings = "s2"
	if sum := Run(context.Background(), repos, opts); sum.Scanned != 1 {
		t.Errorf("settings change: %+v", sum)
	}
	repos[0].Properties = map[string]string{"team": "y"}
	if sum := Run(context.Background(), repos, opts); sum.Scanned != 1 {
		t.Errorf("property change: %+v", sum)
	}
	// New HEAD sha invalidates too.
	fc.head = func(string) (string, error) { return sha2, nil }
	fc.cloneSHA = func(string) string { return sha2 }
	if sum := Run(context.Background(), repos, opts); sum.Scanned != 1 {
		t.Errorf("head change: %+v", sum)
	}
}

func TestRunHeadMovedBetweenLsRemoteAndClone(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	out := t.TempDir()
	fc := &fakeCloner{cloneSHA: func(string) string { return sha2 }} // ls-remote says sha1
	repos := []discover.Repo{remoteRepo("a")}
	opts := Options{OutDir: out, Cloner: fc, Cache: cache.Cache{Dir: cacheDir}}
	if sum := Run(context.Background(), repos, opts); sum.Scanned != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if r := readFacts(t, out, "acme/a"); r.Commit != sha2 {
		t.Errorf("commit = %s, want the cloned %s", r.Commit, sha2)
	}
	// The cache is keyed on the scanned commit (sha2), so a later run that
	// sees sha2 on the remote hits the cache.
	fc.head = func(string) (string, error) { return sha2, nil }
	if sum := Run(context.Background(), repos, opts); sum.Cached != 1 || fc.clones.Load() != 1 {
		t.Errorf("summary = %+v clones=%d", sum, fc.clones.Load())
	}
	// And one that still sees sha1 does not.
	fc.head = nil
	if sum := Run(context.Background(), repos, opts); sum.Scanned != 1 {
		t.Errorf("summary = %+v", sum)
	}
}

func TestRunErrorsRecorded(t *testing.T) {
	t.Parallel()
	headErr := errors.New("no such remote")
	cloneErr := errors.New("clone exploded")
	fc := &fakeCloner{
		head: func(url string) (string, error) {
			if strings.Contains(url, "/badhead.git") {
				return "", headErr
			}
			return sha1, nil
		},
	}
	// One repo fails at RemoteHead, one at Clone, one succeeds.
	var mu sync.Mutex
	progress := map[string]error{}
	opts := Options{OutDir: t.TempDir(), Cloner: fc, Cache: cache.Cache{Dir: t.TempDir()}, Concurrency: 2,
		Progress: func(repo string, _ bool, err error) { mu.Lock(); progress[repo] = err; mu.Unlock() }}
	repos := []discover.Repo{remoteRepo("badhead"), remoteRepo("ok")}
	sum := Run(context.Background(), repos, opts)
	if sum.Scanned != 1 || len(sum.Failed) != 1 || !errors.Is(sum.Failed["acme/badhead"], headErr) {
		t.Fatalf("summary = %+v", sum)
	}
	if progress["acme/badhead"] == nil || progress["acme/ok"] != nil || len(progress) != 2 {
		t.Errorf("progress = %v", progress)
	}

	fc.cloneErr = cloneErr
	sum = Run(context.Background(), []discover.Repo{remoteRepo("c")}, opts)
	if !errors.Is(sum.Failed["acme/c"], cloneErr) || sum.Scanned != 0 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestRunNoCloner(t *testing.T) {
	t.Parallel()
	sum := Run(context.Background(), []discover.Repo{remoteRepo("a")}, Options{OutDir: t.TempDir()})
	if err := sum.Failed["acme/a"]; err == nil || !strings.Contains(err.Error(), "no cloner") {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestRunWriteAndCachePutFailures(t *testing.T) {
	t.Parallel()
	// OutDir is a file so facts.Write fails.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fc := &fakeCloner{}
	sum := Run(context.Background(), []discover.Repo{remoteRepo("a")}, Options{
		OutDir: filepath.Join(blocker, "out"), Cloner: fc, Cache: cache.Cache{Dir: t.TempDir()},
	})
	if sum.Failed["acme/a"] == nil {
		t.Fatalf("want write failure: %+v", sum)
	}
	// Cache dir is a file so Put fails.
	sum = Run(context.Background(), []discover.Repo{remoteRepo("b")}, Options{
		OutDir: t.TempDir(), Cloner: fc, Cache: cache.Cache{Dir: blocker},
	})
	if sum.Failed["acme/b"] == nil {
		t.Fatalf("want cache failure: %+v", sum)
	}
}

func TestRunCachedWriteFailure(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	fc := &fakeCloner{}
	repos := []discover.Repo{remoteRepo("a")}
	if sum := Run(context.Background(), repos, Options{OutDir: t.TempDir(), Cloner: fc, Cache: cache.Cache{Dir: cacheDir}}); sum.Scanned != 1 {
		t.Fatal(sum)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(blocker, nil, 0o644)
	sum := Run(context.Background(), repos, Options{OutDir: filepath.Join(blocker, "x"), Cloner: fc, Cache: cache.Cache{Dir: cacheDir}})
	if sum.Failed["acme/a"] == nil || sum.Cached != 0 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestRunNoCacheDir(t *testing.T) {
	t.Parallel()
	fc := &fakeCloner{}
	repos := []discover.Repo{remoteRepo("a")}
	opts := Options{OutDir: t.TempDir(), Cloner: fc} // zero cache disables caching
	for range 2 {
		if sum := Run(context.Background(), repos, opts); sum.Scanned != 1 {
			t.Fatalf("summary = %+v", sum)
		}
	}
	if fc.clones.Load() != 2 {
		t.Errorf("clones = %d, want 2", fc.clones.Load())
	}
}

func gitCheckout(t *testing.T, dir string, withCommit bool) string {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte("variable \"v\" {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if withCommit {
		run("add", ".")
		run("commit", "-q", "-m", "x")
	}
	return dir
}

func TestRunLocalPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a := gitCheckout(t, filepath.Join(mkdir(t, root, "a")), true)
	b := mkdir(t, root, "b") // not a git repo: no commit, never cached
	if err := os.WriteFile(filepath.Join(b, "main.tf"), []byte("variable \"v\" {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repos := []discover.Repo{
		{FullName: "local/a", Org: "local", Name: "a", LocalPath: a},
		{FullName: "local/b", Org: "local", Name: "b", LocalPath: b},
	}
	fc := &fakeCloner{}
	opts := Options{OutDir: t.TempDir(), Cloner: fc, Cache: cache.Cache{Dir: t.TempDir()}, Concurrency: 2}
	for i := range 2 {
		sum := Run(context.Background(), repos, opts)
		wantCached := 0
		if i == 1 {
			wantCached = 1 // only the git checkout can be keyed by commit
		}
		if sum.Cached != wantCached || sum.Scanned != 2-wantCached || len(sum.Failed) != 0 {
			t.Fatalf("run %d summary = %+v", i, sum)
		}
	}
	if fc.clones.Load() != 0 || fc.heads.Load() != 0 {
		t.Errorf("local paths must not touch the cloner: clones=%d heads=%d", fc.clones.Load(), fc.heads.Load())
	}
	ra := readFacts(t, opts.OutDir, "local/a")
	if len(ra.Commit) != 40 || len(ra.Modules) != 1 {
		t.Errorf("facts a = %+v", ra)
	}
	if rb := readFacts(t, opts.OutDir, "local/b"); rb.Commit != "" {
		t.Errorf("facts b commit = %q", rb.Commit)
	}
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunLocalPathUncommittedRepo(t *testing.T) {
	t.Parallel()
	dir := gitCheckout(t, t.TempDir(), false) // git repo with no commits
	sum := Run(context.Background(), []discover.Repo{{FullName: "local/x", Org: "local", Name: "x", LocalPath: dir}}, Options{
		OutDir: t.TempDir(), Cache: cache.Cache{Dir: t.TempDir()},
	})
	if sum.Scanned != 1 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestRunConcurrency(t *testing.T) {
	t.Parallel()
	var repos []discover.Repo
	for i := range 12 {
		repos = append(repos, remoteRepo(fmt.Sprintf("r%02d", i)))
	}
	fc := &fakeCloner{delay: 30 * time.Millisecond}
	var mu sync.Mutex
	seen := map[string]bool{}
	out := t.TempDir()
	sum := Run(context.Background(), repos, Options{
		OutDir: out, Cloner: fc, Cache: cache.Cache{Dir: t.TempDir()}, Concurrency: 4,
		Progress: func(repo string, _ bool, _ error) { mu.Lock(); seen[repo] = true; mu.Unlock() },
	})
	if sum.Scanned != 12 || len(sum.Failed) != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(seen) != 12 {
		t.Errorf("progress calls for %d repos", len(seen))
	}
	if m := fc.maxFlight.Load(); m < 2 || m > 4 {
		t.Errorf("max concurrent clones = %d, want in [2,4]", m)
	}
	files, _ := filepath.Glob(filepath.Join(out, "*.json"))
	if len(files) != 12 {
		t.Errorf("facts files = %d", len(files))
	}
}

func TestRunDefaultConcurrencyIsSerial(t *testing.T) {
	t.Parallel()
	var repos []discover.Repo
	for i := range 4 {
		repos = append(repos, remoteRepo(fmt.Sprintf("r%d", i)))
	}
	fc := &fakeCloner{delay: 5 * time.Millisecond}
	sum := Run(context.Background(), repos, Options{OutDir: t.TempDir(), Cloner: fc})
	if sum.Scanned != 4 || fc.maxFlight.Load() != 1 {
		t.Errorf("summary = %+v maxFlight=%d", sum, fc.maxFlight.Load())
	}
}

func TestRunContextCancelled(t *testing.T) {
	t.Parallel()
	t.Run("before start", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		fc := &fakeCloner{}
		sum := Run(ctx, []discover.Repo{remoteRepo("a"), remoteRepo("b")}, Options{OutDir: t.TempDir(), Cloner: fc, Concurrency: 2})
		if sum.Scanned != 0 || fc.clones.Load() != 0 {
			t.Fatalf("summary = %+v clones=%d", sum, fc.clones.Load())
		}
		for repo, err := range sum.Failed {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s: %v", repo, err)
			}
		}
	})
	t.Run("mid run", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var repos []discover.Repo
		for i := range 20 {
			repos = append(repos, remoteRepo(fmt.Sprintf("r%02d", i)))
		}
		fc := &fakeCloner{delay: 10 * time.Second}
		done := make(chan Summary, 1)
		go func() {
			done <- Run(ctx, repos, Options{OutDir: t.TempDir(), Cloner: fc, Concurrency: 2})
		}()
		for fc.clones.Load() < 2 {
			time.Sleep(time.Millisecond)
		}
		cancel()
		select {
		case sum := <-done:
			if sum.Scanned != 0 {
				t.Errorf("scanned = %d", sum.Scanned)
			}
			if len(sum.Failed) < 2 || len(sum.Failed) >= 20 {
				t.Errorf("failed = %d, want the in-flight repos only (the rest are never dispatched)", len(sum.Failed))
			}
			for repo, err := range sum.Failed {
				if !errors.Is(err, context.Canceled) {
					t.Errorf("%s: %v", repo, err)
				}
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return after cancellation")
		}
	})
}

func TestRunEmpty(t *testing.T) {
	t.Parallel()
	sum := Run(context.Background(), nil, Options{OutDir: t.TempDir()})
	if sum.Scanned != 0 || sum.Cached != 0 || sum.Failed == nil || len(sum.Failed) != 0 {
		t.Fatalf("summary = %+v", sum)
	}
}

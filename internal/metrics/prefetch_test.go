package metrics

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/source"
)

// slowLister records calls and how many ran at once.
type slowLister struct {
	mu       sync.Mutex
	calls    []string
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (s *slowLister) Versions(_ context.Context, kind, addr string) ([]string, error) {
	n := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	s.mu.Lock()
	s.calls = append(s.calls, kind+" "+addr)
	s.mu.Unlock()
	return []string{"1.0.0"}, nil
}

func TestPrefetchLooksUpEachAddressOnceInParallel(t *testing.T) {
	t.Parallel()
	call := func(kind, addr, ver string) facts.ModuleCall {
		return facts.ModuleCall{SourceKind: kind, Address: addr, Version: ver}
	}
	calls := []facts.ModuleCall{
		call(source.KindRegistry, "registry.terraform.io/a/b/aws", "1.0.0"),
		call(source.KindRegistry, "registry.terraform.io/a/b/aws", "~> 1.0"), // same address
		call(source.KindPrivateRegistry, "tfe.acme.io/acme/vpc/aws", "2.0.0"),
		call(source.KindGit, "github.com/acme/tf-dns", "v1.4.0"),
		call(source.KindGit, "github.com/acme/tf-sha", "0123456789abcdef0123456789abcdef01234567"), // SHA: skipped
		call(source.KindRegistry, "registry.terraform.io/c/d/aws", ""),                             // unpinned: skipped
		call(source.KindLocal, "modules/vpc", ""),
		call(source.KindHTTP, "example.com/m.zip", "1.0.0"),
	}
	for i := range 8 {
		calls = append(calls, call(source.KindRegistry, "registry.terraform.io/x/m"+string(rune('a'+i))+"/aws", "1.0.0"))
	}
	results := []facts.ScanResult{{Modules: []facts.ModuleFacts{{ModuleCalls: calls}}}}
	l := &slowLister{}
	Prefetch(context.Background(), results, l, 4)
	sort.Strings(l.calls)
	if len(l.calls) != 11 {
		t.Fatalf("calls = %v, want 11 distinct lookups", l.calls)
	}
	for i := 1; i < len(l.calls); i++ {
		if l.calls[i] == l.calls[i-1] {
			t.Errorf("duplicate lookup %s", l.calls[i])
		}
	}
	if p := l.peak.Load(); p < 2 || p > 4 {
		t.Errorf("peak concurrency %d, want 2..4", p)
	}
}

func TestPrefetchNilListerAndCancel(t *testing.T) {
	t.Parallel()
	Prefetch(context.Background(), nil, nil, 4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := &slowLister{}
	results := []facts.ScanResult{{Modules: []facts.ModuleFacts{{ModuleCalls: []facts.ModuleCall{
		{SourceKind: source.KindRegistry, Address: "r/a/b/c", Version: "1.0.0"},
	}}}}}
	Prefetch(ctx, results, l, 0)
	if len(l.calls) != 0 {
		t.Errorf("cancelled prefetch made calls: %v", l.calls)
	}
}

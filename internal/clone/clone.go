// Package clone fetches repos cheaply: a shallow clone with a sparse checkout
// limited to Terraform files and CODEOWNERS. It shells out to git.
package clone

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rossedman/tfmetrics/internal/registry"
)

// SparsePatterns are the files checked out from each repo.
var SparsePatterns = []string{"*.tf", "*.tf.json", "*.tofu", "*.tofu.json", "CODEOWNERS"}

// Git runs git with optional token auth for one host. Transient failures
// (throttling, server errors, dropped connections) are retried with
// exponential backoff and jitter.
type Git struct {
	Token string // sent to the clone URL's host; never placed in the URL
	// Retries caps retries of a transient failure; 0 means DefaultRetries,
	// negative means none.
	Retries int
	// BaseDelay is the first backoff; it doubles per retry. 0 means 2s.
	BaseDelay time.Duration
	// Sleep is replaceable for tests.
	Sleep func(ctx context.Context, d time.Duration) error
}

// DefaultRetries is how often a transient git failure is retried.
const DefaultRetries = 3

// transientMarkers are stderr fragments of git failures worth retrying.
var transientMarkers = []string{
	"429", "too many requests", "500", "502", "503", "504",
	"internal server error", "bad gateway", "service unavailable", "gateway timeout",
	"timed out", "timeout", "connection reset", "connection refused", "early eof",
	"unexpected disconnect", "rpc failed", "remote end hung up", "could not resolve host",
	"temporary failure", "tls handshake",
}

// Transient reports whether a git error looks like a temporary failure.
func Transient(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, m := range transientMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// retry runs f until it succeeds, fails permanently, or retries run out.
// before is called ahead of each retry (for example to clean a directory).
func (g Git) retry(ctx context.Context, f func() error, before func()) error {
	retries := g.Retries
	if retries == 0 {
		retries = DefaultRetries
	}
	base := g.BaseDelay
	if base <= 0 {
		base = 2 * time.Second
	}
	sleep := g.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	for attempt := 0; ; attempt++ {
		err := f()
		if err == nil || attempt >= retries || ctx.Err() != nil || !Transient(err) {
			return err
		}
		d := base << attempt
		d = d/2 + time.Duration(rand.Float64()*float64(d/2)) //nolint:gosec // G404: jitter needs no cryptographic randomness
		if err := sleep(ctx, d); err != nil {
			return err
		}
		if before != nil {
			before()
		}
	}
}

// sleepCtx waits for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// env returns base plus what a git command against rawURL needs, and whether
// it sends the token.
func (g Git) env(base []string, rawURL string) ([]string, bool) {
	env := append(base, "GIT_TERMINAL_PROMPT=0")
	if g.Token == "" {
		return env, false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return env, false
	}
	return registry.AuthEnv(env, u.Host, g.Token), true
}

// authMarkers are stderr fragments of git failures caused by rejected or
// missing credentials.
var authMarkers = []string{"could not read username", "authentication failed", "terminal prompts disabled"}

// authFailed reports whether git's stderr says the server wanted credentials.
func authFailed(stderr string) bool {
	s := strings.ToLower(stderr)
	for _, m := range authMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// run runs git and returns stdout, folding stderr into the error.
func (g Git) run(ctx context.Context, rawURL string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	var sendsToken bool
	cmd.Env, sendsToken = g.env(cmd.Environ(), rawURL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if authFailed(msg) {
			if sendsToken {
				msg += " (the server rejected the GitHub token; check that it is valid and can read this repo)"
			} else {
				msg += " (the repo is private or does not exist; set a GitHub token that can read it)"
			}
		}
		return nil, fmt.Errorf("git %s: %w: %s", subcommand(args), err, msg)
	}
	return out, nil
}

// subcommand returns the git subcommand in args, skipping a leading "-C dir".
func subcommand(args []string) string {
	if len(args) > 2 && args[0] == "-C" {
		return args[2]
	}
	return args[0]
}

// RemoteHead returns the commit SHA that ref (or HEAD when empty) points to on
// the remote, without cloning.
func (g Git) RemoteHead(ctx context.Context, rawURL, ref string) (string, error) {
	var sha string
	err := g.retry(ctx, func() error {
		var err error
		sha, err = g.remoteHead(ctx, rawURL, ref)
		return err
	}, nil)
	return sha, err
}

// remoteHead does one attempt of RemoteHead.
func (g Git) remoteHead(ctx context.Context, rawURL, ref string) (string, error) {
	want := "HEAD"
	if ref != "" {
		want = ref
	}
	out, err := g.run(ctx, rawURL, "ls-remote", rawURL, want)
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		sha, name, ok := strings.Cut(sc.Text(), "\t")
		if !ok {
			continue
		}
		if name == want || name == "refs/heads/"+want || name == "refs/tags/"+want {
			return sha, nil
		}
	}
	if isSHA(ref) {
		return ref, nil
	}
	return "", fmt.Errorf("ref %q not found on %s", want, redact(rawURL))
}

// isSHA reports whether s looks like a full commit SHA.
func isSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// Clone shallow-clones rawURL at ref (default branch when empty) into dir with
// a sparse checkout, and returns the checked-out commit.
func (g Git) Clone(ctx context.Context, rawURL, ref, dir string) (string, error) {
	var sha string
	err := g.retry(ctx, func() error {
		var err error
		sha, err = g.clone(ctx, rawURL, ref, dir)
		return err
	}, func() { emptyDir(dir) })
	return sha, err
}

// emptyDir removes everything inside dir, so a clone can be retried into it.
func emptyDir(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

// clone does one attempt of Clone.
func (g Git) clone(ctx context.Context, rawURL, ref, dir string) (string, error) {
	args := []string{"clone", "--depth", "1", "--filter=blob:none", "--no-checkout", "--quiet"}
	if ref != "" && !isSHA(ref) {
		args = append(args, "--branch", ref)
	}
	if _, err := g.run(ctx, rawURL, append(args, rawURL, dir)...); err != nil {
		return "", err
	}
	if isSHA(ref) {
		if _, err := g.run(ctx, rawURL, "-C", dir, "fetch", "--depth", "1", "--filter=blob:none", "--quiet", "origin", ref); err != nil {
			return "", err
		}
	}
	if _, err := g.run(ctx, rawURL, append([]string{"-C", dir, "sparse-checkout", "set", "--no-cone"}, SparsePatterns...)...); err != nil {
		return "", err
	}
	target := "HEAD"
	if isSHA(ref) {
		target = "FETCH_HEAD"
	}
	if _, err := g.run(ctx, rawURL, "-C", dir, "checkout", "--quiet", target); err != nil {
		return "", err
	}
	out, err := g.run(ctx, rawURL, "-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// redact strips credentials from a URL for error messages.
func redact(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil {
		return rawURL
	}
	u.User = nil
	return u.String()
}

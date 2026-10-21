// Package clone fetches repos cheaply: a shallow clone with a sparse checkout
// limited to Terraform files and CODEOWNERS. It shells out to git.
package clone

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/rossedman/tfmetrics/internal/registry"
)

// SparsePatterns are the files checked out from each repo.
var SparsePatterns = []string{"*.tf", "*.tf.json", "*.tofu", "*.tofu.json", "CODEOWNERS"}

// Git runs git with optional token auth for one host.
type Git struct {
	Token string // sent to the clone URL's host; never placed in the URL
}

// env returns the environment for a git command against rawURL.
func (g Git) env(cmd *exec.Cmd, rawURL string) []string {
	env := append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	if g.Token == "" {
		return env
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return env
	}
	return append(env, registry.AuthEnv(u.Host, g.Token)...)
}

// run runs git and returns stdout, folding stderr into the error.
func (g Git) run(ctx context.Context, rawURL string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = g.env(cmd, rawURL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", subcommand(args), err, strings.TrimSpace(stderr.String()))
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

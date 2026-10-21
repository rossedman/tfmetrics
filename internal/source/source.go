// Package source classifies Terraform module sources and normalizes them into
// addresses, so calls to the same module match across repos however they are
// written.
package source

import (
	"net/url"
	"regexp"
	"strings"
)

// Source kinds.
const (
	KindLocal           = "local"
	KindRegistry        = "registry"
	KindPrivateRegistry = "private_registry"
	KindGit             = "git"
	KindHTTP            = "http"
	KindS3              = "s3"
	KindGCS             = "gcs"
	KindUnknown         = "unknown"
)

// PublicRegistry is the default host for registry sources without a host.
const PublicRegistry = "registry.terraform.io"

// publicRegistries are registry hosts treated as public rather than private.
var publicRegistries = map[string]bool{
	PublicRegistry:          true,
	"registry.opentofu.org": true,
}

var (
	registryPart = `[0-9A-Za-z](?:[0-9A-Za-z-_]{0,62}[0-9A-Za-z])?`
	// registryRe matches [host/]namespace/name/provider[//subdir].
	registryRe = regexp.MustCompile(`^(?:([0-9A-Za-z.-]+(?::\d+)?)/)?(` + registryPart + `)/(` + registryPart + `)/([0-9a-z]{1,64})(//.*)?$`)
	shaRe      = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
	semverRe   = regexp.MustCompile(`^v?\d+\.\d+\.\d+([-+][0-9A-Za-z.-]+)?$`)
	verSegRe   = regexp.MustCompile(`(^|[/_-])v?\d+\.\d+\.\d+([/._-]|$)`)
	scpGitRe   = regexp.MustCompile(`^(?:[\w.-]+@)?([\w.-]+):(.+)$`)
)

// Ref is a classified module source.
type Ref struct {
	Kind    string // one of the Kind* constants
	Address string // normalized address
	Ref     string // git ref, if any
}

// Normalizer classifies sources using org-specific settings.
type Normalizer struct {
	// InternalRegistry is the host of the org's private registry, if any.
	InternalRegistry string
	aliases          map[string]string
}

// NewNormalizer returns a Normalizer. aliases maps a source (in any form) to the
// address it should be reported as.
func NewNormalizer(internalRegistry string, aliases map[string]string) *Normalizer {
	n := &Normalizer{InternalRegistry: strings.ToLower(internalRegistry), aliases: map[string]string{}}
	for from, to := range aliases {
		r := n.classify(from)
		n.aliases[r.Address] = to
	}
	return n
}

// Classify returns the kind, normalized address and git ref of a module source.
func (n *Normalizer) Classify(src string) Ref {
	r := n.classify(src)
	if to, ok := n.aliases[r.Address]; ok {
		r.Address = to
		if r.Kind == KindGit || r.Kind == KindHTTP {
			// An alias points at a registry address; classify the target's kind.
			t := n.classify(to)
			if t.Kind == KindRegistry || t.Kind == KindPrivateRegistry {
				r.Kind = t.Kind
			}
		}
	}
	return r
}

// classify does the work of Classify without applying aliases.
func (n *Normalizer) classify(src string) Ref {
	s := strings.TrimSpace(src)
	switch {
	case s == "":
		return Ref{Kind: KindUnknown}
	case strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || s == "." || s == "..":
		return Ref{Kind: KindLocal, Address: cleanLocal(s)}
	}

	if forced, rest, ok := splitForced(s); ok {
		switch forced {
		case "git":
			return gitRef(rest)
		case "s3":
			return Ref{Kind: KindS3, Address: stripQuery(stripScheme(rest))}
		case "gcs":
			return Ref{Kind: KindGCS, Address: stripQuery(stripScheme(rest))}
		case "http", "https":
			return Ref{Kind: KindHTTP, Address: stripQuery(stripScheme(rest))}
		default:
			return Ref{Kind: KindUnknown, Address: s}
		}
	}

	if m := registryRe.FindStringSubmatch(s); m != nil && !looksLikeVCSHost(m[1]) {
		host := strings.ToLower(m[1])
		kind := KindRegistry
		if host == "" {
			host = PublicRegistry
		} else if !publicRegistries[host] {
			kind = KindPrivateRegistry
		}
		addr := host + "/" + strings.ToLower(m[2]) + "/" + strings.ToLower(m[3]) + "/" + m[4] + m[5]
		return Ref{Kind: kind, Address: addr}
	}

	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "github.com/") || strings.HasPrefix(lower, "bitbucket.org/"):
		return gitRef(s)
	case strings.HasPrefix(lower, "git@") || strings.HasPrefix(lower, "ssh://"):
		return gitRef(s)
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		u, err := url.Parse(s)
		if err != nil {
			return Ref{Kind: KindHTTP, Address: s}
		}
		h := strings.ToLower(u.Hostname())
		switch {
		case strings.HasSuffix(h, ".amazonaws.com"):
			return Ref{Kind: KindS3, Address: stripQuery(stripScheme(s))}
		case h == "www.googleapis.com" && strings.HasPrefix(u.Path, "/storage/"):
			return Ref{Kind: KindGCS, Address: stripQuery(stripScheme(s))}
		case h == "github.com" || h == "bitbucket.org" || strings.HasSuffix(u.Path, ".git"):
			return gitRef(s)
		}
		return Ref{Kind: KindHTTP, Address: stripQuery(stripScheme(s))}
	}
	return Ref{Kind: KindUnknown, Address: s}
}

// looksLikeVCSHost reports whether a registry-shaped host is really a VCS host
// (github.com/org/repo/x is a git shorthand with a subpath, not a registry).
func looksLikeVCSHost(host string) bool {
	h := strings.ToLower(host)
	return h == "github.com" || h == "bitbucket.org" || h == "gitlab.com"
}

// splitForced splits a forced-getter prefix such as git:: from the rest.
func splitForced(s string) (string, string, bool) {
	i := strings.Index(s, "::")
	if i <= 0 {
		return "", "", false
	}
	getter := strings.ToLower(s[:i])
	for _, c := range getter {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return "", "", false
		}
	}
	return getter, s[i+2:], true
}

// gitRef normalizes a git source to host/path[//subdir] and extracts ?ref=.
func gitRef(s string) Ref {
	ref := ""
	body, query, _ := strings.Cut(s, "?")
	if query != "" {
		if q, err := url.ParseQuery(query); err == nil {
			ref = q.Get("ref")
		}
	}
	// Separate the //subdir from the repo URL, ignoring the scheme's "//".
	sub := ""
	scheme := ""
	if i := strings.Index(body, "://"); i >= 0 {
		scheme, body = body[:i], body[i+3:]
	}
	if i := strings.Index(body, "//"); i >= 0 {
		body, sub = body[:i], body[i:]
	}
	var host, path string
	if scheme != "" {
		hp := body
		if at := strings.LastIndex(hp, "@"); at >= 0 && at < strings.Index(hp+"/", "/") {
			hp = hp[at+1:]
		}
		host, path, _ = strings.Cut(hp, "/")
		if h, _, ok := strings.Cut(host, ":"); ok {
			host = h
		}
	} else if m := scpGitRe.FindStringSubmatch(body); m != nil && !strings.Contains(strings.SplitN(body, ":", 2)[0], "/") {
		host, path = m[1], m[2]
	} else {
		host, path, _ = strings.Cut(body, "/")
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	addr := strings.ToLower(host)
	if path != "" {
		addr += "/" + strings.ToLower(path)
	}
	return Ref{Kind: KindGit, Address: addr + sub, Ref: ref}
}

// stripScheme removes a URL scheme.
func stripScheme(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		return s[i+3:]
	}
	return s
}

// stripQuery removes a URL query string.
func stripQuery(s string) string {
	b, _, _ := strings.Cut(s, "?")
	return b
}

// cleanLocal tidies a local path without resolving it.
func cleanLocal(s string) string {
	return strings.TrimSuffix(s, "/")
}

// Pinning returns how tightly a call is pinned: exact, range or none. Local
// calls return "" because they have no version.
func Pinning(r Ref, version string) string {
	switch r.Kind {
	case KindLocal:
		return ""
	case KindRegistry, KindPrivateRegistry:
		return constraintPinning(version)
	case KindGit:
		switch {
		case r.Ref == "":
			return "none"
		case shaRe.MatchString(r.Ref) || semverRe.MatchString(r.Ref):
			return "exact"
		default:
			return "range"
		}
	default:
		if r.Ref != "" || verSegRe.MatchString(r.Address) {
			return "exact"
		}
		return "none"
	}
}

// constraintPinning classifies a version constraint string.
func constraintPinning(c string) string {
	c = strings.TrimSpace(c)
	if c == "" {
		return "none"
	}
	if strings.Contains(c, ",") {
		return "range"
	}
	v := strings.TrimSpace(strings.TrimPrefix(c, "="))
	if !strings.HasPrefix(v, "=") && semverRe.MatchString(v) {
		return "exact"
	}
	return "range"
}

// Version returns the version to record for a call: the version constraint for
// registry sources, the git ref for git sources.
func Version(r Ref, version string) string {
	if r.Kind == KindGit || (r.Ref != "" && version == "") {
		return r.Ref
	}
	return strings.TrimSpace(version)
}

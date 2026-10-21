package discover

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// HeadBatch is how many repos one GraphQL query asks about. A query of 100
// aliased repository lookups costs one point of the 5,000-point hourly budget.
const HeadBatch = 100

// HeadSHAs returns the default-branch HEAD commit of each named repo in org,
// keyed by name, using one GraphQL query per HeadBatch repos. Repos that are
// empty or not found are left out. On error, the heads found so far are
// still returned.
func (g *GitHub) HeadSHAs(ctx context.Context, org string, names []string) (map[string]string, error) {
	out := make(map[string]string, len(names))
	for start := 0; start < len(names); start += HeadBatch {
		batch := names[start:min(start+HeadBatch, len(names))]
		if err := g.headBatch(ctx, org, batch, out); err != nil {
			return out, err
		}
	}
	return out, nil
}

// headQuery builds the aliased query for one batch.
func headQuery(org string, names []string) string {
	var b strings.Builder
	b.WriteString("query{")
	for i, n := range names {
		o, _ := json.Marshal(org)
		nm, _ := json.Marshal(n)
		fmt.Fprintf(&b, "r%d:repository(owner:%s,name:%s){name defaultBranchRef{target{oid}}}", i, o, nm)
	}
	b.WriteString("}")
	return b.String()
}

// headBatch runs one query and adds its results to out. A RATE_LIMITED error
// is retried; the transport has recorded the reset and waits for it.
func (g *GitHub) headBatch(ctx context.Context, org string, names []string, out map[string]string) error {
	body, _ := json.Marshal(map[string]string{"query": headQuery(org, names)})
	for attempt := 0; ; attempt++ {
		var resp struct {
			Data map[string]*struct {
				Name             string `json:"name"`
				DefaultBranchRef *struct {
					Target struct {
						OID string `json:"oid"`
					} `json:"target"`
				} `json:"defaultBranchRef"`
			} `json:"data"`
			Errors []struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := g.postGraphQL(ctx, body, &resp); err != nil {
			return err
		}
		limited := false
		for _, e := range resp.Errors {
			if e.Type == "RATE_LIMITED" {
				limited = true
			}
		}
		if limited && attempt < 3 {
			continue
		}
		for _, r := range resp.Data {
			if r != nil && r.DefaultBranchRef != nil && r.DefaultBranchRef.Target.OID != "" {
				out[r.Name] = r.DefaultBranchRef.Target.OID
			}
		}
		if len(resp.Data) == 0 && len(resp.Errors) > 0 {
			return fmt.Errorf("graphql: %s", resp.Errors[0].Message)
		}
		return nil
	}
}

// postGraphQL sends one GraphQL request and decodes the response.
func (g *GitHub) postGraphQL(ctx context.Context, body []byte, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.graphqlURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "tfmetrics")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("graphql: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

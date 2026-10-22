package app

import (
	"strings"
	"testing"
)

// FuzzLoadConfig feeds arbitrary YAML through viper into LoadConfig. Whatever
// the input, it must not panic, and a config it accepts has every default
// applied.
func FuzzLoadConfig(f *testing.F) {
	f.Add(fullYAML)
	f.Add("")
	f.Add("scan:\n  concurrency: -3\n")
	f.Add("hygiene:\n  weights: { pinned: 50 }\n")
	f.Add("modules:\n  aliases:\n    \"git::https://github.com/acme/tf-vpc.git\": tfe.acme.internal/acme/vpc/aws\n")
	f.Add("modules:\n  registries:\n    - host: gitlab.com\n      token_env: GITLAB_TOKEN\n    - host: \"\"\n")
	f.Add("sinks:\n  - openmetrics: { pushgateway: http://pushgw:9091 }\n")
	f.Fuzz(func(t *testing.T, yaml string) {
		v := NewViper()
		v.SetConfigType("yaml")
		if err := v.ReadConfig(strings.NewReader(yaml)); err != nil {
			return // not YAML; viper rejects it before LoadConfig
		}
		c, err := LoadConfig(v)
		if err != nil {
			return
		}
		if c.Scan.Concurrency <= 0 || len(c.Hygiene.Weights) == 0 || c.Source.GitHub.TokenEnv == "" || c.Owners.Property == "" {
			t.Fatalf("accepted config missing defaults: %+v", c)
		}
	})
}

func BenchmarkLoadConfig(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		v := NewViper()
		v.SetConfigType("yaml")
		if err := v.ReadConfig(strings.NewReader(fullYAML)); err != nil {
			b.Fatal(err)
		}
		if _, err := LoadConfig(v); err != nil {
			b.Fatal(err)
		}
	}
}

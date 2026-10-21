package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestMain lets the test binary double as the tfmetrics command.
func TestMain(m *testing.M) {
	if os.Getenv("TFMETRICS_TEST_RUN_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func runMain(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "TFMETRICS_TEST_RUN_MAIN=1", "HOME="+cmd.Dir, "XDG_CONFIG_HOME="+cmd.Dir)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

func TestMainVersion(t *testing.T) {
	out, _, code := runMain(t, "version")
	if code != 0 || !strings.HasPrefix(out, "tfmetrics ") {
		t.Errorf("code=%d out=%q", code, out)
	}
}

func TestMainErrorExitsOne(t *testing.T) {
	_, errs, code := runMain(t, "explain", "does-not-exist.json")
	if code != 1 || !strings.HasPrefix(errs, "error: ") {
		t.Errorf("code=%d stderr=%q", code, errs)
	}
}

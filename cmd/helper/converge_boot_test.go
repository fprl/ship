package helper

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestBootConvergenceContinuesAfterOneEnvironmentFails(t *testing.T) {
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())
	oldEnvs, oldConverge, oldLog := bootEnvs, bootConverge, bootLog
	t.Cleanup(func() { bootEnvs, bootConverge, bootLog = oldEnvs, oldConverge, oldLog })
	bootEnvs = func() ([]appEnvStatus, error) {
		return []appEnvStatus{{App: "api", Env: "production"}, {App: "web", Env: "production"}}, nil
	}
	var converged []string
	bootConverge = func(app, env string) (convergeResult, error) {
		if app == "api" {
			return convergeResult{}, errors.New("broken envelope")
		}
		converged = append(converged, app+"/"+env)
		return convergeResult{}, nil
	}
	var log strings.Builder
	bootLog = func(format string, args ...any) { log.WriteString(fmt.Sprintf(format, args...)); log.WriteByte('\n') }
	if err := runBootConvergence(); err != nil {
		t.Fatal(err)
	}
	if len(converged) != 1 || converged[0] != "web/production" {
		t.Fatalf("converged=%v, want remaining env; log=%s", converged, log.String())
	}
	if !strings.Contains(log.String(), "api") || !strings.Contains(log.String(), "web") {
		t.Fatalf("boot log=%q", log.String())
	}
}

func TestBootConvergenceEnumerationFailureIsFatal(t *testing.T) {
	oldEnvs := bootEnvs
	t.Cleanup(func() { bootEnvs = oldEnvs })
	bootEnvs = func() ([]appEnvStatus, error) { return nil, errors.New("glob failed") }
	if err := runBootConvergence(); err == nil || !strings.Contains(err.Error(), "glob failed") {
		t.Fatalf("error=%v, want enumeration failure", err)
	}
}

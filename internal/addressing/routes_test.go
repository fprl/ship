package addressing

import (
	"testing"

	"github.com/fprl/ship/internal/config"
)

func TestPrimaryURLUsesOneDeterministicRankingRule(t *testing.T) {
	routes := map[string]config.Route{
		"redirect": {Host: "old.example.com", Redirect: "https://new.example.com"},
		"api":      {Host: "api.example.com", Process: "api"},
		"web-path": {Host: "www.example.com", Path: "/app", Process: "web"},
		"web":      {Host: "www.example.com", Process: "web"},
	}
	url, ok := PrimaryURL(routes, false)
	if !ok || url != "https://www.example.com" {
		t.Fatalf("primary = %q, %v", url, ok)
	}
}

func TestPlanRoutesSynthesizesAndCarriesPrimaryURL(t *testing.T) {
	ctx := &config.AppContext{AppName: "api", Processes: map[string]config.Process{"web": {}}}
	plan, err := PlanRoutes(ctx, "production", Options{BoxIP: "203.0.113.7"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RewritesManifest || !plan.NoConfiguredDomain || plan.PrimaryURL != "https://api.203-0-113-7.sslip.io" {
		t.Fatalf("plan = %+v", plan)
	}
}

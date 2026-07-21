package kernel

import (
	"strings"
	"testing"
)

func TestRegistryRejectsDuplicateCommandPaths(t *testing.T) {
	registry := NewRegistry([]Definition{
		{ID: "first", Operations: []Operation{testOperation([]string{"app", "apply"})}},
		{ID: "second", Operations: []Operation{testOperation([]string{"app", "apply"})}},
	})

	err := registry.Freeze()
	if err == nil || !strings.Contains(err.Error(), "duplicate command path") || !strings.Contains(err.Error(), "app apply") {
		t.Fatalf("Freeze() error = %v, want named duplicate command path", err)
	}
}

func TestRegistryRejectsPrefixAmbiguousCommandPaths(t *testing.T) {
	registry := NewRegistry([]Definition{
		{ID: "first", Operations: []Operation{testOperation([]string{"app"})}},
		{ID: "second", Operations: []Operation{testOperation([]string{"app", "apply"})}},
	})

	err := registry.Freeze()
	if err == nil || !strings.Contains(err.Error(), "prefix-ambiguous") || !strings.Contains(err.Error(), "app apply") {
		t.Fatalf("Freeze() error = %v, want named prefix ambiguity", err)
	}
}

func TestRegistryAllowsPrefixRelatedPathsWithDisjointExposures(t *testing.T) {
	longer := testOperation([]string{"app", "apply", "extra"})
	longer.Exposure = ExposureInternal
	longer.Authorization = Authorization{}
	registry := NewRegistry([]Definition{
		{ID: "first", Operations: []Operation{testOperation([]string{"app", "apply"})}},
		{ID: "second", Operations: []Operation{longer}},
	})

	if err := registry.Freeze(); err != nil {
		t.Fatalf("Freeze() error = %v, want prefix-related paths with disjoint exposures accepted", err)
	}
}

func TestRegistryRejectsClientOperationWithoutAuthorization(t *testing.T) {
	tests := []struct {
		name          string
		authorization Authorization
	}{
		{name: "empty permission", authorization: Authorization{Target: func(any) (any, error) { return "target", nil }}},
		{name: "nil target extractor", authorization: Authorization{Permission: "app.apply"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry([]Definition{{
				ID: "apps",
				Operations: []Operation{{
					Path:          []string{"app", "apply"},
					Exposure:      ExposureClient,
					Authorization: test.authorization,
				}},
			}})

			err := registry.Freeze()
			if err == nil || !strings.Contains(err.Error(), "app apply") || !strings.Contains(err.Error(), "apps") {
				t.Fatalf("Freeze() error = %v, want named missing authorization metadata", err)
			}
		})
	}
}

func TestRegistryRejectsOverlappingOwnershipPaths(t *testing.T) {
	registry := NewRegistry([]Definition{
		{ID: "first", OwnershipPaths: []string{"modules/first"}},
		{ID: "second", OwnershipPaths: []string{"modules/first/internal"}},
	})

	err := registry.Freeze()
	if err == nil || !strings.Contains(err.Error(), "overlaps") || !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") {
		t.Fatalf("Freeze() error = %v, want named ownership overlap", err)
	}
}

func TestRegistryReadSurfaceIsDefensiveAndExposureAware(t *testing.T) {
	operation := testOperation([]string{"app", "apply"})
	operation.ErrorCodes = []string{"bad-request"}
	registry := NewRegistry([]Definition{{ID: "apps", Operations: []Operation{operation}}})
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}

	operations := registry.Operations()
	operations[0].Path[0] = "changed"
	operations[0].ErrorCodes[0] = "changed"
	lookup, ok := registry.Lookup([]string{"app", "apply"})
	if !ok || lookup.Path[0] != "app" || lookup.ErrorCodes[0] != "bad-request" {
		t.Fatalf("registry read surface was not defensive: %#v, %v", lookup, ok)
	}
	if !registry.PathAllowed([]string{"app", "apply"}, ExposureClient) || registry.PathAllowed([]string{"app", "apply", "extra"}, ExposureClient) || registry.PathAllowed([]string{"app", "apply"}, ExposureInternal) {
		t.Fatal("PathAllowed returned the wrong result")
	}
}

func testOperation(path []string) Operation {
	return Operation{
		Path:          path,
		Exposure:      ExposureClient,
		Authorization: Authorization{Permission: "operation.use", Target: func(request any) (any, error) { return request, nil }},
		Handler:       func(Context, any) error { return nil },
	}
}

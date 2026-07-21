package kernel

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Registry stores validated module definitions and their operations. A
// registry is writable only through construction and becomes immutable when
// Freeze succeeds.
type Registry struct {
	definitions []Definition
	operations  []registeredOperation
	byPath      map[string]registeredOperation
	frozen      bool
}

type registeredOperation struct {
	moduleID  string
	operation Operation
}

// NewRegistry makes a registry from definitions. The input is copied, so
// later changes to the caller's slices cannot change the registry.
func NewRegistry(definitions []Definition) *Registry {
	return &Registry{definitions: cloneDefinitions(definitions)}
}

// Freeze validates the definitions and publishes the immutable read surface.
// Calling Freeze again after a successful freeze is harmless.
func (r *Registry) Freeze() error {
	if r == nil {
		return fmt.Errorf("nil registry")
	}
	if r.frozen {
		return nil
	}

	if err := validateDefinitions(r.definitions); err != nil {
		return err
	}

	operations := make([]registeredOperation, 0)
	byPath := make(map[string]registeredOperation)
	for _, definition := range r.definitions {
		for _, operation := range definition.Operations {
			registered := registeredOperation{
				moduleID:  definition.ID,
				operation: cloneOperation(operation),
			}
			operations = append(operations, registered)
			byPath[pathKey(operation.Path)] = registered
		}
	}
	sort.Slice(operations, func(i, j int) bool {
		return comparePaths(operations[i].operation.Path, operations[j].operation.Path) < 0
	})
	r.operations = operations
	r.byPath = byPath
	r.definitions = nil
	r.frozen = true
	return nil
}

// Operations returns every registered operation in deterministic path order.
// All slices in the returned values are independent copies.
func (r *Registry) Operations() []Operation {
	if r == nil || !r.frozen {
		return nil
	}
	out := make([]Operation, len(r.operations))
	for i, registered := range r.operations {
		out[i] = cloneOperation(registered.operation)
	}
	return out
}

// Lookup returns the operation registered at an exact command path.
func (r *Registry) Lookup(path []string) (Operation, bool) {
	if r == nil || !r.frozen {
		return Operation{}, false
	}
	registered, ok := r.byPath[pathKey(path)]
	if !ok {
		return Operation{}, false
	}
	return cloneOperation(registered.operation), true
}

// PathAllowed reports whether a command path is registered for an exposure
// class. The path must match the complete registered operation path.
func (r *Registry) PathAllowed(path []string, exposure Exposure) bool {
	if r == nil || !r.frozen || exposure == 0 {
		return false
	}
	operation, ok := r.byPath[pathKey(path)]
	return ok && operation.operation.Exposure&exposure != 0
}

func (r *Registry) lookup(path []string) (registeredOperation, bool) {
	if r == nil || !r.frozen {
		return registeredOperation{}, false
	}
	operation, ok := r.byPath[pathKey(path)]
	return operation, ok
}

func validateDefinitions(definitions []Definition) error {
	owners := make([]ownership, 0)
	moduleIDs := make(map[string]struct{})
	for _, definition := range definitions {
		if err := validateDefinition(definition); err != nil {
			return err
		}
		if _, exists := moduleIDs[definition.ID]; exists {
			return fmt.Errorf("duplicate module id %q", definition.ID)
		}
		moduleIDs[definition.ID] = struct{}{}
		for _, ownerPath := range definition.OwnershipPaths {
			cleanPath := filepath.ToSlash(filepath.Clean(ownerPath))
			for _, previous := range owners {
				if previous.moduleID != definition.ID && ownershipOverlap(previous.path, cleanPath) {
					return fmt.Errorf("ownership path %q in module %q overlaps path %q in module %q", ownerPath, definition.ID, previous.path, previous.moduleID)
				}
			}
			owners = append(owners, ownership{moduleID: definition.ID, path: cleanPath})
		}
	}

	registered := make([]registeredOperation, 0)
	seen := make(map[string]registeredOperation)
	for _, definition := range definitions {
		for _, operation := range definition.Operations {
			key := pathKey(operation.Path)
			if previous, ok := seen[key]; ok {
				return fmt.Errorf("duplicate command path %q in modules %q and %q", formatPath(operation.Path), previous.moduleID, definition.ID)
			}
			current := registeredOperation{moduleID: definition.ID, operation: operation}
			for _, previous := range registered {
				if previous.operation.Exposure&operation.Exposure != 0 && (strictPathPrefix(previous.operation.Path, operation.Path) || strictPathPrefix(operation.Path, previous.operation.Path)) {
					return fmt.Errorf("prefix-ambiguous command path %q in module %q conflicts with %q in module %q", formatPath(operation.Path), definition.ID, formatPath(previous.operation.Path), previous.moduleID)
				}
			}
			seen[key] = current
			registered = append(registered, current)
		}
	}
	return nil
}

func validateDefinition(definition Definition) error {
	if definition.ID == "" {
		return fmt.Errorf("module definition has empty id")
	}
	if definition.ID == "." || definition.ID == ".." || strings.ContainsAny(definition.ID, `/\\`) {
		return fmt.Errorf("module %q has invalid id", definition.ID)
	}
	for _, ownerPath := range definition.OwnershipPaths {
		cleanPath := filepath.Clean(ownerPath)
		if ownerPath == "" || filepath.IsAbs(ownerPath) || cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
			return fmt.Errorf("module %q has invalid ownership path %q", definition.ID, ownerPath)
		}
	}
	for _, operation := range definition.Operations {
		if len(operation.Path) == 0 {
			return fmt.Errorf("module %q has operation with empty command path", definition.ID)
		}
		for _, token := range operation.Path {
			if token == "" || strings.ContainsRune(token, '\x00') {
				return fmt.Errorf("module %q has invalid command path %q", definition.ID, formatPath(operation.Path))
			}
		}
		if operation.Exposure&ExposureClient != 0 && (operation.Authorization.Permission == "" || operation.Authorization.Target == nil) {
			return fmt.Errorf("module %q operation %q is client-exposed but missing authorization metadata", definition.ID, formatPath(operation.Path))
		}
	}
	return nil
}

type ownership struct {
	moduleID string
	path     string
}

func ownershipOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func strictPathPrefix(prefix, path []string) bool {
	if len(prefix) >= len(path) {
		return false
	}
	for i := range prefix {
		if prefix[i] != path[i] {
			return false
		}
	}
	return true
}

func pathKey(path []string) string {
	return strings.Join(path, "\x00")
}

func formatPath(path []string) string {
	return strings.Join(path, " ")
}

func comparePaths(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

func cloneDefinitions(definitions []Definition) []Definition {
	out := make([]Definition, len(definitions))
	for i, definition := range definitions {
		out[i] = Definition{
			ID:             definition.ID,
			OwnershipPaths: append([]string(nil), definition.OwnershipPaths...),
			Operations:     make([]Operation, len(definition.Operations)),
		}
		for j, operation := range definition.Operations {
			out[i].Operations[j] = cloneOperation(operation)
		}
	}
	return out
}

func cloneOperation(operation Operation) Operation {
	operation.Path = append([]string(nil), operation.Path...)
	operation.ErrorCodes = append([]string(nil), operation.ErrorCodes...)
	return operation
}

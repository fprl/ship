package kernel

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type testBinder struct {
	actor Actor
	seen  *[]string
}

func (b testBinder) Bind(any) (Actor, error) {
	*b.seen = append(*b.seen, "bind")
	return b.actor, nil
}

type testAuthorizer struct {
	allow      bool
	seen       *[]string
	actor      Actor
	permission Permission
	target     any
}

func (a *testAuthorizer) Authorize(actor Actor, permission Permission, target any) error {
	*a.seen = append(*a.seen, "authorize")
	a.actor = actor
	a.permission = permission
	a.target = target
	if !a.allow {
		return errors.New("denied")
	}
	return nil
}

func TestDispatcherDenialHappensBeforeHandler(t *testing.T) {
	seen := []string{}
	authorizer := &testAuthorizer{seen: &seen}
	handlerCalled := false
	operation := testOperation([]string{"app", "apply"})
	operation.Authorization.Target = func(any) (any, error) {
		seen = append(seen, "extract")
		return "environment/one", nil
	}
	operation.Handler = func(Context, any) error {
		handlerCalled = true
		seen = append(seen, "handler")
		return nil
	}
	registry := NewRegistry([]Definition{{ID: "apps", Operations: []Operation{operation}}})
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewDispatcher(registry, testBinder{actor: Actor{ID: "member-1"}, seen: &seen}, authorizer, "/var/lib/ship")
	if err != nil {
		t.Fatal(err)
	}

	err = dispatcher.Dispatch(DispatchRequest{Path: []string{"app", "apply"}, Exposure: ExposureClient, Request: "request"})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("Dispatch() error = %v, want denial", err)
	}
	if handlerCalled {
		t.Fatal("handler was called after authorization denial")
	}
	if want := []string{"bind", "extract", "authorize"}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("admission order = %v, want %v", seen, want)
	}
}

func TestDispatcherPopulatesContext(t *testing.T) {
	seen := []string{}
	authorizer := &testAuthorizer{allow: true, seen: &seen}
	var got Context
	operation := testOperation([]string{"app", "status"})
	operation.Handler = func(ctx Context, request any) error {
		got = ctx
		if request != "request" {
			t.Errorf("handler request = %v, want request", request)
		}
		return nil
	}
	registry := NewRegistry([]Definition{{ID: "apps", Operations: []Operation{operation}}})
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewDispatcher(registry, testBinder{actor: Actor{ID: "member-1"}, seen: &seen}, authorizer, "/var/lib/ship/state")
	if err != nil {
		t.Fatal(err)
	}

	if err := dispatcher.Dispatch(DispatchRequest{
		Path:      []string{"app", "status"},
		Exposure:  ExposureClient,
		Request:   "request",
		RequestID: "req-123",
	}); err != nil {
		t.Fatal(err)
	}
	if got.Actor != (Actor{ID: "member-1"}) || got.RequestID != "req-123" || got.Target != "request" || got.StateDir != "/var/lib/ship/state/apps" {
		t.Fatalf("handler context = %#v", got)
	}
	if got.Context == nil {
		t.Fatal("handler context has no cancellation context")
	}
	if authorizer.actor != got.Actor || authorizer.permission != "operation.use" || authorizer.target != "request" {
		t.Fatalf("authorizer inputs = actor %v permission %q target %v", authorizer.actor, authorizer.permission, authorizer.target)
	}
}

package kernel

import (
	"context"
	"fmt"
	"path/filepath"
)

// IdentityBinder binds a transport credential to an Actor.
type IdentityBinder interface {
	// Bind authenticates a transport credential and returns its actor.
	Bind(credential any) (Actor, error)
}

// Authorizer is the single authorization decision point in the kernel.
type Authorizer interface {
	// Authorize allows the actor to use a permission for a target, or returns a denial error.
	Authorize(actor Actor, permission Permission, target any) error
}

// DispatchRequest contains the transport-neutral inputs for one dispatch.
type DispatchRequest struct {
	// Path is the exact operation path to dispatch.
	Path []string
	// Exposure is the caller class and must be allowed by the operation.
	Exposure Exposure
	// Credential is the transport credential to bind to an Actor.
	Credential any
	// Request is the operation-specific request value.
	Request any
	// RequestID identifies the request.
	RequestID string
	// Context carries cancellation and deadlines into the handler.
	Context context.Context
	// Emit receives events from the handler.
	Emit EventEmitter
}

// Dispatcher performs admission and invokes operations from a frozen
// registry.
type Dispatcher struct {
	registry   *Registry
	binder     IdentityBinder
	authorizer Authorizer
	stateRoot  string
}

// NewDispatcher constructs a dispatcher backed by a frozen registry.
func NewDispatcher(registry *Registry, binder IdentityBinder, authorizer Authorizer, stateRoot string) (*Dispatcher, error) {
	if registry == nil {
		return nil, fmt.Errorf("dispatcher requires a registry")
	}
	if !registry.frozen {
		return nil, fmt.Errorf("dispatcher requires a frozen registry")
	}
	if binder == nil {
		return nil, fmt.Errorf("dispatcher requires an identity binder")
	}
	if authorizer == nil {
		return nil, fmt.Errorf("dispatcher requires an authorizer")
	}
	return &Dispatcher{
		registry:   registry,
		binder:     binder,
		authorizer: authorizer,
		stateRoot:  stateRoot,
	}, nil
}

// Dispatch resolves, binds, authorizes, and invokes one operation. A handler
// is never called if identity binding, target extraction, or authorization
// fails.
func (d *Dispatcher) Dispatch(request DispatchRequest) error {
	registered, ok := d.registry.lookup(request.Path)
	if !ok {
		return fmt.Errorf("operation %q is not registered", formatPath(request.Path))
	}
	if request.Exposure == 0 || registered.operation.Exposure&request.Exposure == 0 {
		return fmt.Errorf("operation %q is not allowed for exposure %d", formatPath(request.Path), request.Exposure)
	}

	actor, err := d.binder.Bind(request.Credential)
	if err != nil {
		return fmt.Errorf("bind identity for operation %q: %w", formatPath(request.Path), err)
	}

	target := any(nil)
	if registered.operation.Authorization.Target != nil {
		target, err = registered.operation.Authorization.Target(request.Request)
		if err != nil {
			return fmt.Errorf("extract authorization target for operation %q: %w", formatPath(request.Path), err)
		}
	} else if registered.operation.Authorization.Permission != "" {
		return fmt.Errorf("operation %q has permission %q but no target extractor", formatPath(request.Path), registered.operation.Authorization.Permission)
	}

	if registered.operation.Authorization.Permission != "" {
		if err := d.authorizer.Authorize(actor, registered.operation.Authorization.Permission, target); err != nil {
			return fmt.Errorf("authorize operation %q: %w", formatPath(request.Path), err)
		}
	}

	if registered.operation.Handler == nil {
		return fmt.Errorf("operation %q has no handler", formatPath(request.Path))
	}
	requestContext := request.Context
	if requestContext == nil {
		requestContext = context.Background()
	}
	emitter := request.Emit
	if emitter == nil {
		emitter = discardEmitter{}
	}
	return registered.operation.Handler(Context{
		Context:   requestContext,
		Actor:     actor,
		RequestID: request.RequestID,
		Target:    target,
		Emit:      emitter,
		StateDir:  filepath.Join(d.stateRoot, registered.moduleID),
	}, request.Request)
}

type discardEmitter struct{}

func (discardEmitter) Emit(Event) error { return nil }

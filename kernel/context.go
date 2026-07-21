package kernel

import "context"

// Actor is the identity bound to a request by an IdentityBinder.
type Actor struct {
	// ID is the stable identity value.
	ID string
}

// Event is the minimal event payload handlers may emit.
type Event struct {
	// Name identifies the event.
	Name string
	// Message is the human-readable event detail.
	Message string
}

// EventEmitter receives events produced by a handler.
type EventEmitter interface {
	// Emit sends one event to the request's event sink.
	Emit(Event) error
}

// Context is the mechanism-only context supplied to an operation handler.
// Its state directory is namespaced to the owning module.
type Context struct {
	context.Context

	// Actor is the bound request identity.
	Actor Actor
	// RequestID identifies the request being handled.
	RequestID string
	// Target is the authorization target extracted from the request.
	Target any
	// Emit sends a handler event to the request's event sink.
	Emit EventEmitter
	// StateDir is the owning module's namespaced state directory.
	StateDir string
}

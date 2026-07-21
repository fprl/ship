package kernel

// Exposure identifies a caller class that may reach an operation. Values are
// bit flags because an operation may be reachable by more than one class.
type Exposure uint8

const (
	// ExposureClient identifies normal client requests.
	ExposureClient Exposure = 1 << iota
	// ExposureRepair identifies repair requests.
	ExposureRepair
	// ExposureInternal identifies requests made by internal jobs.
	ExposureInternal
	// ExposureGateway identifies gateway requests.
	ExposureGateway
)

// Permission is an authorization class declared by an operation. The kernel
// does not interpret its value.
type Permission string

// TargetExtractor names the authorization target in an operation request.
type TargetExtractor func(request any) (target any, err error)

// Authorization declares the permission and target required by an operation.
type Authorization struct {
	// Permission is the opaque authorization class required by the operation.
	Permission Permission
	// Target extracts the object the permission applies to from a request.
	Target TargetExtractor
}

// Handler is the server-side function invoked after admission succeeds.
type Handler func(Context, any) error

// ClientAdapter describes how an operation is surfaced by a client CLI.
type ClientAdapter struct {
	// Name is the client command name.
	Name string
	// Usage is the concise command usage string.
	Usage string
	// Description is the client-facing command description.
	Description string
}

// Docs contains the short and long help text for an operation.
type Docs struct {
	// Short is a one-line summary.
	Short string
	// Long is the detailed help text.
	Long string
}

// Operation is one remote verb and all metadata needed to project it into
// protocol, authorization, and client surfaces.
type Operation struct {
	// Path is the fixed server command path, excluding flags and arguments.
	Path []string
	// Exposure identifies caller classes that may reach the operation.
	Exposure Exposure
	// Authorization declares the permission and target for admission.
	Authorization Authorization
	// Handler runs the operation after identity binding and authorization.
	Handler Handler
	// ClientAdapter describes the client CLI representation.
	ClientAdapter ClientAdapter
	// Docs contains short and long operation help.
	Docs Docs
	// ErrorCodes lists errors the operation is expected to return.
	ErrorCodes []string
}

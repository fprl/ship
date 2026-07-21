package kernel

// Definition is the data a module contributes to the kernel registry.
type Definition struct {
	// ID is the stable module identifier.
	ID string
	// OwnershipPaths are repo-relative path prefixes owned by the module.
	OwnershipPaths []string
	// Operations are the remote verbs contributed by the module.
	Operations []Operation
}

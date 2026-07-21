package remoteprotocol

import "github.com/fprl/ship/kernel"

// commandAuthorizationTarget keeps the target's naming contract in the
// operation metadata while preserving the request for the dispatcher that
// will eventually become the enforcement point.
type commandAuthorizationTarget struct {
	Name    string
	Request any
}

func namedTarget(name string) kernel.TargetExtractor {
	return func(request any) (any, error) {
		return commandAuthorizationTarget{Name: name, Request: request}, nil
	}
}

// The declared permission is the operation's primary authorization class.
// A few commands conditionally escalate beyond it today (production data
// save/restore adds box mutation; share only demands more when rotating);
// those escalations are policy over (permission, target) and stay enforced
// in the helper until dispatch is wired, at which point they become the
// single authorizer's policy — not a second declared class.
func operation(path []string, exposure kernel.Exposure, permission kernel.Permission, target string) kernel.Operation {
	return kernel.Operation{
		Path:     path,
		Exposure: exposure,
		Authorization: kernel.Authorization{
			Permission: permission,
			Target:     namedTarget(target),
		},
	}
}

func unguardedOperation(path []string, exposure kernel.Exposure) kernel.Operation {
	return kernel.Operation{Path: path, Exposure: exposure}
}

var commandRegistry = func() *kernel.Registry {
	registry := kernel.NewRegistry([]kernel.Definition{
		{
			ID: "remote-app",
			Operations: []kernel.Operation{
				operation([]string{"app", "apply"}, ExposureClient, "ship", "app/env"),
				operation([]string{"app", "converge"}, ExposureClient, "ship", "app/env"),
				operation([]string{"app", "data", "fork"}, ExposureClient, "data", "app/env"),
				operation([]string{"app", "data", "reset"}, ExposureClient, "data", "app/env"),
				operation([]string{"app", "data", "restore"}, ExposureClient, "data", "app/env"),
				operation([]string{"app", "data", "save"}, ExposureClient, "data_save", "app/env"),
				operation([]string{"app", "destroy"}, ExposureClient, "box_mutation", "box app"),
				operation([]string{"app", "destroy-env"}, ExposureClient, "rm", "app/env"),
				operation([]string{"app", "exec"}, ExposureClient, "exec", "app/env"),
				operation([]string{"app", "logs"}, ExposureClient, "read", "app/env"),
				operation([]string{"app", "ls"}, ExposureClient, "read", "box"),
				operation([]string{"app", "preflight"}, ExposureClient, "read", "app/env"),
				operation([]string{"app", "preview", "pin"}, ExposureClient, "preview_pin", "preview branch"),
				operation([]string{"app", "preview", "resolve"}, ExposureClient, "read", "preview branch"),
				operation([]string{"app", "preview", "resolve-or-create"}, ExposureClient, "ship", "preview branch"),
				operation([]string{"app", "preview", "share"}, ExposureClient, "share", "app/env"),
				operation([]string{"app", "preview", "unpin"}, ExposureClient, "preview_pin", "preview branch"),
				operation([]string{"app", "rollback"}, ExposureClient, "rollback", "app/env"),
				operation([]string{"app", "secret", "list"}, ExposureClient, "secret_read", "app/env"),
				operation([]string{"app", "secret", "rm"}, ExposureClient, "secret_remove", "app/env"),
				operation([]string{"app", "secret", "set"}, ExposureClient, "secret_set", "app/env"),
				operation([]string{"app", "setup-env"}, ExposureClient, "ship", "app/env"),
				operation([]string{"app", "status"}, ExposureClient, "read", "app/env"),
				operation([]string{"app", "why"}, ExposureClient, "read", "app/env"),
			},
		},
		{
			ID: "remote-box",
			Operations: []kernel.Operation{
				operation([]string{"approval", "grant"}, ExposureClient, "approval", "approval ID"),
				operation([]string{"approval", "ls"}, ExposureClient, "read", "box"),
				operation([]string{"config", "get"}, ExposureClient, "read", "box config"),
				operation([]string{"config", "set"}, ExposureClient, "box_mutation", "box config key"),
				operation([]string{"config", "unset"}, ExposureClient, "box_mutation", "box config key"),
				operation([]string{"doctor"}, ExposureClient, "read", "box"),
				operation([]string{"gc"}, ExposureClient|ExposureInternal, "box_mutation", "box"),
				operation([]string{"key", "add"}, ExposureClient, "member", "box member"),
				operation([]string{"key", "ls"}, ExposureClient, "read", "box"),
				operation([]string{"key", "rename"}, ExposureClient, "member", "box member"),
				operation([]string{"key", "rm"}, ExposureClient, "member", "box member"),
				operation([]string{"key", "role"}, ExposureClient, "member", "box member"),
				operation([]string{"webhook", "clear"}, ExposureClient, "box_mutation", "box webhook"),
				operation([]string{"webhook", "get"}, ExposureClient, "read", "box webhook"),
				operation([]string{"webhook", "set"}, ExposureClient, "box_mutation", "box webhook"),
			},
		},
		{
			ID: "remote-maintenance",
			Operations: []kernel.Operation{
				operation([]string{"version"}, ExposureRepair, "read", "box"),
				operation([]string{"update"}, ExposureRepair, "box_mutation", "box"),
				unguardedOperation([]string{"converge-boot"}, ExposureInternal),
				unguardedOperation([]string{"doctor", "record"}, ExposureInternal),
				unguardedOperation([]string{"env", "reap"}, ExposureInternal),
			},
		},
		{
			ID: "remote-gateway",
			Operations: []kernel.Operation{
				unguardedOperation([]string{"agent-shell"}, ExposureGateway),
				unguardedOperation([]string{"update-local"}, ExposureGateway),
			},
		},
	})
	if err := registry.Freeze(); err != nil {
		panic(err)
	}
	return registry
}()

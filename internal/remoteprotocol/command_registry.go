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
				operation([]string{"app", "apply"}, kernel.ExposureClient, "ship", "app/env"),
				operation([]string{"app", "converge"}, kernel.ExposureClient, "ship", "app/env"),
				operation([]string{"app", "data", "fork"}, kernel.ExposureClient, "data", "app/env"),
				operation([]string{"app", "data", "reset"}, kernel.ExposureClient, "data", "app/env"),
				operation([]string{"app", "data", "restore"}, kernel.ExposureClient, "data", "app/env"),
				operation([]string{"app", "data", "save"}, kernel.ExposureClient, "data_save", "app/env"),
				operation([]string{"app", "destroy"}, kernel.ExposureClient, "box_mutation", "box app"),
				operation([]string{"app", "destroy-env"}, kernel.ExposureClient, "rm", "app/env"),
				operation([]string{"app", "exec"}, kernel.ExposureClient, "exec", "app/env"),
				operation([]string{"app", "logs"}, kernel.ExposureClient, "read", "app/env"),
				operation([]string{"app", "ls"}, kernel.ExposureClient, "read", "box"),
				operation([]string{"app", "preflight"}, kernel.ExposureClient, "read", "app/env"),
				operation([]string{"app", "preview", "pin"}, kernel.ExposureClient, "preview_pin", "preview branch"),
				operation([]string{"app", "preview", "resolve"}, kernel.ExposureClient, "read", "preview branch"),
				operation([]string{"app", "preview", "resolve-or-create"}, kernel.ExposureClient, "ship", "preview branch"),
				operation([]string{"app", "preview", "share"}, kernel.ExposureClient, "share", "app/env"),
				operation([]string{"app", "preview", "unpin"}, kernel.ExposureClient, "preview_pin", "preview branch"),
				operation([]string{"app", "rollback"}, kernel.ExposureClient, "rollback", "app/env"),
				operation([]string{"app", "secret", "list"}, kernel.ExposureClient, "secret_read", "app/env"),
				operation([]string{"app", "secret", "rm"}, kernel.ExposureClient, "secret_remove", "app/env"),
				operation([]string{"app", "secret", "set"}, kernel.ExposureClient, "secret_set", "app/env"),
				operation([]string{"app", "setup-env"}, kernel.ExposureClient, "ship", "app/env"),
				operation([]string{"app", "status"}, kernel.ExposureClient, "read", "app/env"),
				operation([]string{"app", "why"}, kernel.ExposureClient, "read", "app/env"),
			},
		},
		{
			ID: "remote-box",
			Operations: []kernel.Operation{
				operation([]string{"approval", "grant"}, kernel.ExposureClient, "approval", "approval ID"),
				operation([]string{"approval", "ls"}, kernel.ExposureClient, "read", "box"),
				operation([]string{"config", "get"}, kernel.ExposureClient, "read", "box config"),
				operation([]string{"config", "set"}, kernel.ExposureClient, "box_mutation", "box config key"),
				operation([]string{"config", "unset"}, kernel.ExposureClient, "box_mutation", "box config key"),
				operation([]string{"doctor"}, kernel.ExposureClient, "read", "box"),
				operation([]string{"gc"}, kernel.ExposureClient|kernel.ExposureInternal, "box_mutation", "box"),
				operation([]string{"key", "add"}, kernel.ExposureClient, "member", "box member"),
				operation([]string{"key", "ls"}, kernel.ExposureClient, "read", "box"),
				operation([]string{"key", "rename"}, kernel.ExposureClient, "member", "box member"),
				operation([]string{"key", "rm"}, kernel.ExposureClient, "member", "box member"),
				operation([]string{"key", "role"}, kernel.ExposureClient, "member", "box member"),
				operation([]string{"webhook", "clear"}, kernel.ExposureClient, "box_mutation", "box webhook"),
				operation([]string{"webhook", "get"}, kernel.ExposureClient, "read", "box webhook"),
				operation([]string{"webhook", "set"}, kernel.ExposureClient, "box_mutation", "box webhook"),
			},
		},
		{
			ID: "remote-maintenance",
			Operations: []kernel.Operation{
				operation([]string{"version"}, kernel.ExposureRepair, "read", "box"),
				operation([]string{"update"}, kernel.ExposureRepair, "box_mutation", "box"),
				unguardedOperation([]string{"converge-boot"}, kernel.ExposureInternal),
				unguardedOperation([]string{"doctor", "record"}, kernel.ExposureInternal),
				unguardedOperation([]string{"env", "reap"}, kernel.ExposureInternal),
			},
		},
		{
			ID: "remote-gateway",
			Operations: []kernel.Operation{
				unguardedOperation([]string{"agent-shell"}, kernel.ExposureGateway),
				unguardedOperation([]string{"update-local"}, kernel.ExposureGateway),
			},
		},
	})
	if err := registry.Freeze(); err != nil {
		panic(err)
	}
	return registry
}()

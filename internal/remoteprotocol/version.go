// Package remoteprotocol owns Ship's private client-to-box interface.
package remoteprotocol

import (
	"strconv"
	"strings"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/version"
)

// VersionResponse is the stable response returned by `server version --json`.
// Both the client and helper use this type so the version probe cannot drift.
type VersionResponse struct {
	Version      string `json:"version"`
	ShipVersion  string `json:"ship_version"`
	Architecture string `json:"architecture"`
}

// RequireExactVersion enforces Ship's lockstep client/helper contract.
// server is the client-routable box target used in the repair command.
func RequireExactVersion(clientVersion, helperVersion, server string) error {
	clientVersion = strings.TrimSpace(clientVersion)
	helperVersion = strings.TrimSpace(helperVersion)
	if clientVersion == helperVersion {
		return nil
	}

	fields := errcat.Fields{
		"client_version": displayVersion(clientVersion),
		"helper_version": helperVersion,
		"server":         server,
	}
	if clientVersion == "" {
		return errcat.New(errcat.CodeClientBehindHelper, fields)
	}
	if isGitDescribeVersion(clientVersion) || isGitDescribeVersion(helperVersion) {
		return errcat.New(errcat.CodeBoxVersionAmbiguous, fields)
	}
	cmp, ok := version.Compare(helperVersion, clientVersion)
	if ok && cmp < 0 {
		return errcat.New(errcat.CodeBoxHelperBehind, fields)
	}
	if ok && cmp > 0 {
		return errcat.New(errcat.CodeClientBehindHelper, fields)
	}
	return errcat.New(errcat.CodeBoxVersionAmbiguous, fields)
}

func displayVersion(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func isGitDescribeVersion(value string) bool {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	_, prerelease, ok := strings.Cut(value, "-")
	if !ok {
		return false
	}
	parts := strings.Split(prerelease, "-")
	if len(parts) != 2 || !strings.HasPrefix(parts[1], "g") || len(parts[1]) == 1 {
		return false
	}
	_, err := strconv.Atoi(parts[0])
	return err == nil
}

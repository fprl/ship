package errcat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type Code string

const (
	CodeUsageError                        Code = "usage_error"
	CodeManifestInvalid                   Code = "manifest_invalid"
	CodeDockerfileMissing                 Code = "dockerfile_missing"
	CodeOperationFailed                   Code = "operation_failed"
	CodeNotAGitRepo                       Code = "not_a_git_repo"
	CodeDetachedHeadRequiresBranch        Code = "detached_head_requires_branch"
	CodeBranchFlagRequiresDetachedHead    Code = "branch_flag_requires_detached_head"
	CodeUnmappableBranchName              Code = "unmappable_branch_name"
	CodeDirtyWorktree                     Code = "dirty_worktree"
	CodeBehindProduction                  Code = "behind_production"
	CodeSecretScopeConflict               Code = "secret_scope_conflict"
	CodeProductionBranchNotPreview        Code = "production_branch_not_preview"
	CodeDataForkOnProduction              Code = "data_fork_on_production"
	CodeNoPreviewEnv                      Code = "no_preview_env"
	CodeShareOnProduction                 Code = "share_on_production"
	CodeMultiProcessNoWebRoute            Code = "multi_process_no_web_route"
	CodeSecretMissing                     Code = "secret_missing"
	CodeUnknownPreviewBranch              Code = "unknown_preview_branch"
	CodeNoDeploys                         Code = "no_deploys"
	CodeSSHUnreachable                    Code = "ssh_unreachable"
	CodeHostKeyChanged                    Code = "host_key_changed"
	CodeBoxNotInitialized                 Code = "box_not_initialized"
	CodeBoxMissingTool                    Code = "box_missing_tool"
	CodeRemotePreflightFailed             Code = "remote_preflight_failed"
	CodeRemotePreflightAfterPrepareFailed Code = "remote_preflight_after_prepare_failed"
	CodeDeployBlockedLocalChecks          Code = "deploy_blocked_local_checks"
	CodeReleaseCommandFailed              Code = "release_command_failed"
	CodeProbeFailed                       Code = "probe_failed"
	CodeInvalidSecretKey                  Code = "invalid_secret_key"
	CodeLogsFollowJSONConflict            Code = "logs_follow_json_conflict"
	CodeBoxTargetRequired                 Code = "box_target_required"
	CodeInvalidBoxTarget                  Code = "invalid_box_target"
	CodeRmConfirmationRequired            Code = "rm_confirmation_required"
	CodeBoxAppRmConfirmationRequired      Code = "box_app_rm_confirmation_required"
	CodeKeysURLUnavailable                Code = "keys_url_unavailable"
	CodeSSHPublicKeyInvalid               Code = "ssh_public_key_invalid"
	CodeMemberNotFound                    Code = "member_not_found"
	CodeMemberLastKey                     Code = "member_last_key"
	CodeMemberUnknown                     Code = "member_unknown"
	CodeRoleDenied                        Code = "role_denied"
	CodeApprovalRequired                  Code = "approval_required"
	CodeApprovalExpired                   Code = "approval_expired"
	CodeDotenvRejected                    Code = "dotenv_rejected"
	CodeDotenvMalformed                   Code = "dotenv_malformed"
	CodeHostNotInstalled                  Code = "host_not_installed"
	CodeHostInvalid                       Code = "host_invalid"
	CodeMissingTool                       Code = "missing_tool"
	CodeDeployTmpMissing                  Code = "deploy_tmp_missing"
	CodeDeployTmpInvalid                  Code = "deploy_tmp_invalid"
	CodeEnvMissing                        Code = "env_missing"
	CodeEnvInvalid                        Code = "env_invalid"
	CodeIngressInvalid                    Code = "ingress_invalid"
	CodeSecretInvalid                     Code = "secret_invalid"
	CodeSecretReadError                   Code = "secret_read_error"
	CodeDeployKeyMissing                  Code = "deploy_key_missing"
	CodeOperatorKeyMissing                Code = "operator_key_missing"
	CodeSSHPrivateKeyMissing              Code = "ssh_private_key_missing"
	CodeSSHPublicKeyFileMissing           Code = "ssh_public_key_file_missing"
	CodeSSHPublicKeyFileEmpty             Code = "ssh_public_key_file_empty"
	CodeHostInstallRequiresRoot           Code = "host_install_requires_root"
	CodeHostInstallSSHFailed              Code = "host_install_ssh_failed"
	CodeUnsupportedTargetArchitecture     Code = "unsupported_target_architecture"
	CodeHostHelperUnavailable             Code = "host_helper_unavailable"
	CodeHostHelperDownloadFailed          Code = "host_helper_download_failed"
	CodeHostInstallUnsupportedOS          Code = "host_install_unsupported_os"
	CodeHostInstallMissingTool            Code = "host_install_missing_tool"
	CodeHostInstallPermissionDenied       Code = "host_install_permission_denied"
	CodeHostInstallApplyFailed            Code = "host_install_apply_failed"
	CodeDataSnapshotInvalid               Code = "data_snapshot_invalid"
	CodeClientBehindHelper                Code = "client_behind_helper"
	CodeBoxVersionAmbiguous               Code = "box_version_ambiguous"
	CodeBoxSetupRequired                  Code = "box_setup_required"
	CodeBoxConfigKeyUnknown               Code = "box_config_key_unknown"
	CodeBoxConfigValueInvalid             Code = "box_config_value_invalid"
	CodeHostLabelConflict                 Code = "host_label_conflict"
)

type Fields map[string]string

type Entry struct {
	Code                Code
	MessageTemplate     string
	CauseTemplate       string
	RemediationTemplate string
	Defaults            Fields
}

var catalogue = map[Code]Entry{
	CodeUsageError: {
		Code:                CodeUsageError,
		MessageTemplate:     "command usage failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "ship help"},
	},
	CodeManifestInvalid: {
		Code:                CodeManifestInvalid,
		MessageTemplate:     "ship.toml validation failed",
		CauseTemplate:       "{details}",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "fix ship.toml"},
	},
	CodeDockerfileMissing: {
		Code:                CodeDockerfileMissing,
		MessageTemplate:     "Dockerfile is missing",
		CauseTemplate:       "the declared processes need a Dockerfile to build",
		RemediationTemplate: "write a Dockerfile, or declare a [routes] static route in ship.toml",
	},
	CodeOperationFailed: {
		Code:                CodeOperationFailed,
		MessageTemplate:     "operation failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "ship status"},
	},
	CodeNotAGitRepo: {
		Code:                CodeNotAGitRepo,
		MessageTemplate:     "git worktree required",
		CauseTemplate:       "current directory is not inside a Git worktree",
		RemediationTemplate: "git init && git add . && git commit -m \"initial ship app\"",
	},
	CodeDetachedHeadRequiresBranch: {
		Code:                CodeDetachedHeadRequiresBranch,
		MessageTemplate:     "branch resolution failed",
		CauseTemplate:       "HEAD is detached; pass --branch <name> so ship can resolve the environment",
		RemediationTemplate: "{command}",
	},
	CodeBranchFlagRequiresDetachedHead: {
		Code:                CodeBranchFlagRequiresDetachedHead,
		MessageTemplate:     "branch resolution failed",
		CauseTemplate:       "--branch is only accepted on ship when HEAD is detached",
		RemediationTemplate: "ship",
	},
	CodeUnmappableBranchName: {
		Code:                CodeUnmappableBranchName,
		MessageTemplate:     "branch resolution failed",
		CauseTemplate:       "branch {branch} does not produce a valid environment name",
		RemediationTemplate: "git branch -m <new-name>",
	},
	CodeDirtyWorktree: {
		Code:                CodeDirtyWorktree,
		MessageTemplate:     "Production ship failed",
		CauseTemplate:       "production branch {branch} has uncommitted changes",
		RemediationTemplate: "git add . && git commit -m \"<message>\"",
	},
	CodeBehindProduction: {
		Code:                CodeBehindProduction,
		MessageTemplate:     "Production ship failed",
		CauseTemplate:       "deployed commit {deployed} {detail}",
		RemediationTemplate: "git pull",
	},
	CodeSecretScopeConflict: {
		Code:                CodeSecretScopeConflict,
		MessageTemplate:     "secret scope is invalid",
		CauseTemplate:       "--preview and --branch cannot be combined",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "ship secret set KEY --preview"},
	},
	CodeProductionBranchNotPreview: {
		Code:                CodeProductionBranchNotPreview,
		MessageTemplate:     "preview command failed",
		CauseTemplate:       "branch {branch} maps to Production",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "ship preview pin <preview-branch>"},
	},
	CodeDataForkOnProduction: {
		Code:                CodeDataForkOnProduction,
		MessageTemplate:     "data command refused on Production",
		CauseTemplate:       "branch {branch} maps to Production; data commands target Preview branches only",
		RemediationTemplate: "git checkout <preview-branch>",
	},
	CodeNoPreviewEnv: {
		Code:                CodeNoPreviewEnv,
		MessageTemplate:     "preview environment lookup failed",
		CauseTemplate:       "no Preview environment exists for branch {branch}",
		RemediationTemplate: "ship",
	},
	CodeShareOnProduction: {
		Code:                CodeShareOnProduction,
		MessageTemplate:     "share command refused on Production",
		CauseTemplate:       "branch {branch} maps to Production; share links are for Preview branches only",
		RemediationTemplate: "git checkout <preview-branch>",
	},
	CodeMultiProcessNoWebRoute: {
		Code:                CodeMultiProcessNoWebRoute,
		MessageTemplate:     "route synthesis failed",
		CauseTemplate:       "manifest declares multiple processes but no [routes] host and no process named \"web\"",
		RemediationTemplate: "fix ship.toml",
	},
	CodeSecretMissing: {
		Code:                CodeSecretMissing,
		MessageTemplate:     "deploy is missing a required secret",
		CauseTemplate:       "missing secret {secret} for {scope}",
		RemediationTemplate: "{command}",
	},
	CodeUnknownPreviewBranch: {
		Code:                CodeUnknownPreviewBranch,
		MessageTemplate:     "preview environment lookup failed",
		CauseTemplate:       "no preview environment is mapped for branch {branch}",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "git checkout <branch> && ship"},
	},
	CodeNoDeploys: {
		Code:                CodeNoDeploys,
		MessageTemplate:     "deploy journal lookup failed",
		CauseTemplate:       "no deploys recorded for {app} ({env})",
		RemediationTemplate: "ship",
	},
	CodeSSHUnreachable: {
		Code:                CodeSSHUnreachable,
		MessageTemplate:     "box preflight failed",
		CauseTemplate:       "SSH failed for {target}: {detail}",
		RemediationTemplate: "ssh {target}",
	},
	CodeHostKeyChanged: {
		Code:                CodeHostKeyChanged,
		MessageTemplate:     "box host key changed",
		CauseTemplate:       "SSH host key for {box} is unknown or changed; if the box was rebuilt, re-establish the pin (ship box forget {box} clears it); if not, investigate before trusting this host",
		RemediationTemplate: "ship box setup <ssh-target>",
	},
	CodeBoxNotInitialized: {
		Code:                CodeBoxNotInitialized,
		MessageTemplate:     "box preflight failed",
		CauseTemplate:       "ship server API is missing at /usr/local/bin/ship on {target}",
		RemediationTemplate: "ship box setup {target}",
	},
	CodeBoxMissingTool: {
		Code:                CodeBoxMissingTool,
		MessageTemplate:     "box preflight failed",
		CauseTemplate:       "required server tool is missing on {target}: {tool}",
		RemediationTemplate: "ship box setup {target}",
	},
	CodeRemotePreflightFailed: {
		Code:                CodeRemotePreflightFailed,
		MessageTemplate:     "deploy preflight failed before upload/build/mutation",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "ship box doctor",
	},
	CodeRemotePreflightAfterPrepareFailed: {
		Code:                CodeRemotePreflightAfterPrepareFailed,
		MessageTemplate:     "deploy preflight failed after preparing the app environment",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "ship box doctor",
	},
	CodeDeployBlockedLocalChecks: {
		Code:                CodeDeployBlockedLocalChecks,
		MessageTemplate:     "deploy blocked by local checks",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "fix local checks", "detail": "local checks reported errors; see stderr above"},
	},
	CodeReleaseCommandFailed: {
		Code:                CodeReleaseCommandFailed,
		MessageTemplate:     "release command failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "ship why",
	},
	CodeProbeFailed: {
		Code:                CodeProbeFailed,
		MessageTemplate:     "probe failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "ship why",
	},
	CodeInvalidSecretKey: {
		Code:                CodeInvalidSecretKey,
		MessageTemplate:     "secret key is invalid",
		CauseTemplate:       "secret key {key} must match ^[A-Za-z_][A-Za-z0-9_]*$",
		RemediationTemplate: "ship secret set KEY",
	},
	CodeLogsFollowJSONConflict: {
		Code:                CodeLogsFollowJSONConflict,
		MessageTemplate:     "logs command is invalid",
		CauseTemplate:       "logs --json cannot be combined with --follow",
		RemediationTemplate: "ship logs",
	},
	CodeBoxTargetRequired: {
		Code:                CodeBoxTargetRequired,
		MessageTemplate:     "target a box",
		CauseTemplate:       "{known_boxes}",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "ship box app ls <box>", "known_boxes": "known boxes (~/.config/ship/known_hosts):\n  none known yet"},
	},
	CodeInvalidBoxTarget: {
		Code:                CodeInvalidBoxTarget,
		MessageTemplate:     "box target is invalid",
		CauseTemplate:       "box target must be a host like 203.0.113.7; remove any user@ prefix",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "ship box app ls 203.0.113.7"},
	},
	CodeRmConfirmationRequired: {
		Code:                CodeRmConfirmationRequired,
		MessageTemplate:     "Production rm confirmation failed",
		CauseTemplate:       "Production rm requires --confirm {app}",
		RemediationTemplate: "ship rm {branch} --confirm {app}",
	},
	CodeBoxAppRmConfirmationRequired: {
		Code:                CodeBoxAppRmConfirmationRequired,
		MessageTemplate:     "box app rm confirmation failed",
		CauseTemplate:       "box app rm requires --confirm {app}",
		RemediationTemplate: "ship box app rm {app} {box} --confirm {app}",
		Defaults:            Fields{"box": "<box>"},
	},
	CodeKeysURLUnavailable: {
		Code:                CodeKeysURLUnavailable,
		MessageTemplate:     "remote SSH key lookup failed",
		CauseTemplate:       "no public SSH keys found at {source}",
		RemediationTemplate: "ship box member add {source} {box} --name {name}",
		Defaults:            Fields{"source": "<https-url>", "box": "<box>", "name": "<name>"},
	},
	CodeSSHPublicKeyInvalid: {
		Code:                CodeSSHPublicKeyInvalid,
		MessageTemplate:     "SSH public key is invalid",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "ship box member add <https-url|key|path> {box} --name <name>",
		Defaults:            Fields{"box": "<box>"},
	},
	CodeMemberNotFound: {
		Code:                CodeMemberNotFound,
		MessageTemplate:     "member rm failed",
		CauseTemplate:       "no authorized keys found for member {name}; current members: {members}",
		RemediationTemplate: "ship box member ls {box}",
		Defaults:            Fields{"box": "<box>"},
	},
	CodeMemberLastKey: {
		Code:                CodeMemberLastKey,
		MessageTemplate:     "member rm refused",
		CauseTemplate:       "removing {name} would remove the last remaining authorized key",
		RemediationTemplate: "ship box member add <https-url|key|path> {box} --name <name>",
		Defaults:            Fields{"box": "<box>"},
	},
	CodeMemberUnknown: {
		Code:                CodeMemberUnknown,
		MessageTemplate:     "member identity is not authorized",
		CauseTemplate:       "fingerprint {fingerprint} is not in authorized_keys",
		RemediationTemplate: "ship box member add <https-url|key|path> {box} --name <name>",
		Defaults:            Fields{"box": "<box>"},
	},
	CodeRoleDenied: {
		Code:                CodeRoleDenied,
		MessageTemplate:     "operation denied",
		CauseTemplate:       "{member} ({role}) cannot {summary}",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "ship status"},
	},
	CodeApprovalRequired: {
		Code:                CodeApprovalRequired,
		MessageTemplate:     "approval required for {summary}",
		CauseTemplate:       "{member} ({role}) requested {summary}; approval id {id}",
		RemediationTemplate: "ship box approval grant {id} {box}",
		Defaults:            Fields{"box": "<box>"},
	},
	CodeApprovalExpired: {
		Code:                CodeApprovalExpired,
		MessageTemplate:     "approval expired",
		CauseTemplate:       "approval {id} expired for {summary}",
		RemediationTemplate: "retry the command to mint a fresh request",
	},
	CodeDotenvRejected: {
		Code:                CodeDotenvRejected,
		MessageTemplate:     "deploy artifact contains dotenv files",
		CauseTemplate:       "refusing to deploy dotenv file: {files}",
		RemediationTemplate: "run ship secret set --from .env, then remove the file (allowed names: .env.example, .env.sample, .env.defaults)",
	},
	CodeDotenvMalformed: {
		Code:                CodeDotenvMalformed,
		MessageTemplate:     "dotenv import failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "ship secret set --from path/to/.env"},
	},
	CodeHostNotInstalled: {
		Code:                CodeHostNotInstalled,
		MessageTemplate:     "host preflight failed",
		CauseTemplate:       "host is not installed",
		RemediationTemplate: "ship box setup <ssh-target>",
	},
	CodeHostInvalid: {
		Code:                CodeHostInvalid,
		MessageTemplate:     "host preflight failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "ship box doctor",
	},
	CodeMissingTool: {
		Code:                CodeMissingTool,
		MessageTemplate:     "host preflight failed",
		CauseTemplate:       "missing host tool: {tool}",
		RemediationTemplate: "ship box setup <ssh-target>",
	},
	CodeDeployTmpMissing: {
		Code:                CodeDeployTmpMissing,
		MessageTemplate:     "host preflight failed",
		CauseTemplate:       "deploy tmp dir is missing: {path}",
		RemediationTemplate: "ship box setup <ssh-target>",
	},
	CodeDeployTmpInvalid: {
		Code:                CodeDeployTmpInvalid,
		MessageTemplate:     "host preflight failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "ship box doctor",
	},
	CodeEnvMissing: {
		Code:                CodeEnvMissing,
		MessageTemplate:     "app environment preflight failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "ship",
	},
	CodeEnvInvalid: {
		Code:                CodeEnvInvalid,
		MessageTemplate:     "app environment preflight failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "ship box doctor",
	},
	CodeIngressInvalid: {
		Code:                CodeIngressInvalid,
		MessageTemplate:     "ingress preflight failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "ship box doctor",
	},
	CodeSecretInvalid: {
		Code:                CodeSecretInvalid,
		MessageTemplate:     "secret preflight failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "ship secret set KEY",
	},
	CodeSecretReadError: {
		Code:                CodeSecretReadError,
		MessageTemplate:     "secret preflight failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "ship box doctor",
	},
	CodeDeployKeyMissing: {
		Code:                CodeDeployKeyMissing,
		MessageTemplate:     "bootstrap SSH key is missing",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "{command}",
		Defaults: Fields{
			"detail":  "provider gave a password; this installs your ship key using it once; hardening then disables password login permanently",
			"command": "ssh-copy-id -i ~/.ssh/ship.pub root@<ip>",
		},
	},
	CodeOperatorKeyMissing: {
		Code:                CodeOperatorKeyMissing,
		MessageTemplate:     "operator SSH key is missing",
		CauseTemplate:       "no SSH public key source found for operator user",
		RemediationTemplate: "{command}",
	},
	CodeSSHPrivateKeyMissing: {
		Code:                CodeSSHPrivateKeyMissing,
		MessageTemplate:     "SSH private key is missing",
		CauseTemplate:       "SSH private key file not found: {path}",
		RemediationTemplate: "{command}",
	},
	CodeSSHPublicKeyFileMissing: {
		Code:                CodeSSHPublicKeyFileMissing,
		MessageTemplate:     "SSH public key file is missing",
		CauseTemplate:       "SSH public key file not found: {path}",
		RemediationTemplate: "{command}",
	},
	CodeSSHPublicKeyFileEmpty: {
		Code:                CodeSSHPublicKeyFileEmpty,
		MessageTemplate:     "SSH public key file is empty",
		CauseTemplate:       "SSH public key file is empty: {path}",
		RemediationTemplate: "{command}",
	},
	CodeHostInstallRequiresRoot: {
		Code:                CodeHostInstallRequiresRoot,
		MessageTemplate:     "local host install needs root",
		CauseTemplate:       "local mode must run as root",
		RemediationTemplate: "{command}",
	},
	CodeHostInstallSSHFailed: {
		Code:                CodeHostInstallSSHFailed,
		MessageTemplate:     "host install SSH failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "{command}",
	},
	CodeUnsupportedTargetArchitecture: {
		Code:                CodeUnsupportedTargetArchitecture,
		MessageTemplate:     "host architecture is unsupported",
		CauseTemplate:       "target architecture {arch} is not supported",
		RemediationTemplate: "ship box setup <amd64-or-arm64-ssh-target>",
	},
	CodeHostHelperUnavailable: {
		Code:                CodeHostHelperUnavailable,
		MessageTemplate:     "host install helper is unavailable",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "SHIP_REPO_ROOT=<path-to-ship-checkout> ship box setup <ssh-target>"},
	},
	CodeHostHelperDownloadFailed: {
		Code:                CodeHostHelperDownloadFailed,
		MessageTemplate:     "host install helper download failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "SHIP_REPO_ROOT=<path-to-ship-checkout> ship box setup <ssh-target>"},
	},
	CodeHostInstallUnsupportedOS: {
		Code:                CodeHostInstallUnsupportedOS,
		MessageTemplate:     "host OS is unsupported",
		CauseTemplate:       "host install requires Ubuntu/Debian apt tooling; missing {tool}",
		RemediationTemplate: "ship box setup <ubuntu-24.04-ssh-target>",
	},
	CodeHostInstallMissingTool: {
		Code:                CodeHostInstallMissingTool,
		MessageTemplate:     "host install dependency is missing",
		CauseTemplate:       "missing required host tool: {tool}",
		RemediationTemplate: "sudo apt-get update && sudo apt-get install -y {tool}",
	},
	CodeHostInstallPermissionDenied: {
		Code:                CodeHostInstallPermissionDenied,
		MessageTemplate:     "host install needs elevated permissions",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "{command}",
	},
	CodeHostInstallApplyFailed: {
		Code:                CodeHostInstallApplyFailed,
		MessageTemplate:     "host provisioning failed",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "{command}",
	},
	CodeDataSnapshotInvalid: {
		Code:                CodeDataSnapshotInvalid,
		MessageTemplate:     "data snapshot is invalid",
		CauseTemplate:       "{detail}",
		RemediationTemplate: "ship data ls",
		Defaults:            Fields{"detail": "snapshot metadata or data payload is invalid"},
	},
	CodeClientBehindHelper: {
		Code:                CodeClientBehindHelper,
		MessageTemplate:     "client is behind the box helper",
		CauseTemplate:       "helper version {helper_version} is newer than client version {client_version}",
		RemediationTemplate: "curl -fsSL https://github.com/fprl/ship/releases/latest/download/install.sh | bash",
	},
	CodeBoxVersionAmbiguous: {
		Code:                CodeBoxVersionAmbiguous,
		MessageTemplate:     "box update cannot order these builds",
		CauseTemplate:       "helper {helper_version} and client {client_version} are different builds of the same release",
		RemediationTemplate: "ship box setup {server}",
	},
	CodeBoxSetupRequired: {
		Code:                CodeBoxSetupRequired,
		MessageTemplate:     "box predates one-command update",
		CauseTemplate:       "this box's helper and sudo rules are older than ship box update",
		RemediationTemplate: "ship box setup {server}",
	},
	CodeBoxConfigKeyUnknown: {
		Code:                CodeBoxConfigKeyUnknown,
		MessageTemplate:     "box config key is unknown",
		CauseTemplate:       "{key} is not a valid box config key; valid keys: {valid}",
		RemediationTemplate: "{command}",
	},
	CodeBoxConfigValueInvalid: {
		Code:                CodeBoxConfigValueInvalid,
		MessageTemplate:     "box config value is invalid",
		CauseTemplate:       "{key}: {detail}",
		RemediationTemplate: "{command}",
	},
	CodeHostLabelConflict: {
		Code:                CodeHostLabelConflict,
		MessageTemplate:     "production hostname collision",
		CauseTemplate:       "app {app} (production) generates host label {label}, already used by {existing_app} ({existing_env})",
		RemediationTemplate: "rename app {app} and deploy again",
	},
}

type Error struct {
	code        Code
	message     string
	cause       string
	remediation string
}

type ErrorObject struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Cause       string `json:"cause"`
	Remediation string `json:"remediation"`
}

type ErrorPayload struct {
	Error ErrorObject `json:"error"`
}

func New(code Code, fields Fields) *Error {
	entry := MustLookup(code)
	merged := mergeFields(entry.Defaults, fields)
	return &Error{
		code:        code,
		message:     renderTemplate(entry.MessageTemplate, merged),
		cause:       renderTemplate(entry.CauseTemplate, merged),
		remediation: renderTemplate(entry.RemediationTemplate, merged),
	}
}

func MustLookup(code Code) Entry {
	entry, ok := catalogue[code]
	if !ok {
		panic(fmt.Sprintf("uncatalogued error code: %s", string(code)))
	}
	return entry
}

func Catalogue() []Entry {
	out := make([]Entry, 0, len(catalogue))
	for _, entry := range catalogue {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Code < out[j].Code
	})
	return out
}

func (c Code) String() string {
	MustLookup(c)
	return string(c)
}

func (e *Error) Error() string {
	return e.Human()
}

func (e *Error) Code() Code {
	return e.code
}

func (e *Error) Message() string {
	return e.message
}

func (e *Error) Cause() string {
	return e.cause
}

func (e *Error) Remediation() string {
	return e.remediation
}

func (e *Error) Human() string {
	return fmt.Sprintf("%s\n%s\nnext: %s", e.message, e.cause, e.remediation)
}

func (e *Error) Object() ErrorObject {
	return ErrorObject{
		Code:        string(e.code),
		Message:     e.message,
		Cause:       e.cause,
		Remediation: e.remediation,
	}
}

func (e *Error) JSONLine() string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ErrorPayload{Error: e.Object()}); err != nil {
		panic(err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func FromObject(obj ErrorObject) *Error {
	code := Code(obj.Code)
	if _, ok := catalogue[code]; !ok {
		cause := obj.Cause
		if cause == "" {
			cause = fmt.Sprintf("received uncatalogued error code %q", obj.Code)
		} else {
			cause = fmt.Sprintf("received uncatalogued error code %q: %s", obj.Code, cause)
		}
		return &Error{
			code:        CodeOperationFailed,
			message:     obj.Message,
			cause:       cause,
			remediation: obj.Remediation,
		}
	}
	return &Error{
		code:        code,
		message:     obj.Message,
		cause:       obj.Cause,
		remediation: obj.Remediation,
	}
}

func ParseJSON(data string) (*Error, bool) {
	data = strings.TrimSpace(data)
	if data == "" {
		return nil, false
	}
	var payload ErrorPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return nil, false
	}
	if payload.Error.Code == "" {
		return nil, false
	}
	return FromObject(payload.Error), true
}

func As(err error) (*Error, bool) {
	var coded *Error
	if errors.As(err, &coded) {
		return coded, true
	}
	return nil, false
}

func Is(err error, code Code) bool {
	coded, ok := As(err)
	return ok && coded.Code() == code
}

func WithCause(err error, cause string) error {
	coded, ok := As(err)
	if !ok {
		return err
	}
	next := *coded
	next.cause = cause
	return &next
}

func mergeFields(defaults, fields Fields) Fields {
	out := Fields{}
	for key, value := range defaults {
		out[key] = value
	}
	for key, value := range fields {
		out[key] = value
	}
	return out
}

func renderTemplate(tmpl string, fields Fields) string {
	var out strings.Builder
	for i := 0; i < len(tmpl); {
		start := strings.IndexByte(tmpl[i:], '{')
		if start < 0 {
			out.WriteString(tmpl[i:])
			break
		}
		start += i
		out.WriteString(tmpl[i:start])
		end := strings.IndexByte(tmpl[start:], '}')
		if end < 0 {
			panic(fmt.Sprintf("unterminated template placeholder in %q", tmpl))
		}
		end += start
		name := tmpl[start+1 : end]
		value, ok := fields[name]
		if !ok {
			panic(fmt.Sprintf("missing template field %q for %q", name, tmpl))
		}
		out.WriteString(value)
		i = end + 1
	}
	return out.String()
}

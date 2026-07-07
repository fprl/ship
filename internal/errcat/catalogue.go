package errcat

import (
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
	CodeOperationFailed                   Code = "operation_failed"
	CodeNotAGitRepo                       Code = "not_a_git_repo"
	CodeDetachedHeadRequiresBranch        Code = "detached_head_requires_branch"
	CodeBranchFlagRequiresDetachedHead    Code = "branch_flag_requires_detached_head"
	CodeUnmappableBranchName              Code = "unmappable_branch_name"
	CodeDirtyWorktree                     Code = "dirty_worktree"
	CodeBehindProduction                  Code = "behind_production"
	CodeSecretScopeConflict               Code = "secret_scope_conflict"
	CodeProductionBranchNotPreview        Code = "production_branch_not_preview"
	CodeMultiProcessNoWebRoute            Code = "multi_process_no_web_route"
	CodeSecretMissing                     Code = "secret_missing"
	CodeUnknownPreviewBranch              Code = "unknown_preview_branch"
	CodeNoDeploys                         Code = "no_deploys"
	CodeSSHUnreachable                    Code = "ssh_unreachable"
	CodeBoxNotInitialized                 Code = "box_not_initialized"
	CodeBoxMissingTool                    Code = "box_missing_tool"
	CodeRemotePreflightFailed             Code = "remote_preflight_failed"
	CodeRemotePreflightAfterPrepareFailed Code = "remote_preflight_after_prepare_failed"
	CodeDeployBlockedLocalChecks          Code = "deploy_blocked_local_checks"
	CodeInvalidSecretKey                  Code = "invalid_secret_key"
	CodeLogsFollowJSONConflict            Code = "logs_follow_json_conflict"
	CodeBoxTargetRequired                 Code = "box_target_required"
	CodeInvalidBoxTarget                  Code = "invalid_box_target"
	CodeRmConfirmationRequired            Code = "rm_confirmation_required"
	CodeDotenvRejected                    Code = "dotenv_rejected"
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
		Defaults:            Fields{"command": "ship pin <preview-branch>"},
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
	CodeBoxNotInitialized: {
		Code:                CodeBoxNotInitialized,
		MessageTemplate:     "box preflight failed",
		CauseTemplate:       "ship server API is missing at /usr/local/bin/ship on {target}",
		RemediationTemplate: "ship box init {target}",
	},
	CodeBoxMissingTool: {
		Code:                CodeBoxMissingTool,
		MessageTemplate:     "box preflight failed",
		CauseTemplate:       "required server tool is missing on {target}: {tool}",
		RemediationTemplate: "ship box init {target}",
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
		MessageTemplate:     "box target is required",
		CauseTemplate:       "this command needs an SSH target outside an app directory",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "ship box ls <ssh-target>"},
	},
	CodeInvalidBoxTarget: {
		Code:                CodeInvalidBoxTarget,
		MessageTemplate:     "box target is invalid",
		CauseTemplate:       "box target must be an SSH target like deploy@example.com",
		RemediationTemplate: "{command}",
		Defaults:            Fields{"command": "ship box ls deploy@example.com"},
	},
	CodeRmConfirmationRequired: {
		Code:                CodeRmConfirmationRequired,
		MessageTemplate:     "Production rm confirmation failed",
		CauseTemplate:       "Production rm requires --confirm {app}",
		RemediationTemplate: "ship rm {branch} --confirm {app}",
	},
	CodeDotenvRejected: {
		Code:                CodeDotenvRejected,
		MessageTemplate:     "deploy artifact contains dotenv files",
		CauseTemplate:       "refusing to deploy dotenv file: {files}",
		RemediationTemplate: "ship --include-dotenv",
	},
	CodeHostNotInstalled: {
		Code:                CodeHostNotInstalled,
		MessageTemplate:     "host preflight failed",
		CauseTemplate:       "host is not installed",
		RemediationTemplate: "ship box init <ssh-target>",
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
		RemediationTemplate: "ship box init <ssh-target>",
	},
	CodeDeployTmpMissing: {
		Code:                CodeDeployTmpMissing,
		MessageTemplate:     "host preflight failed",
		CauseTemplate:       "deploy tmp dir is missing: {path}",
		RemediationTemplate: "ship box init <ssh-target>",
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
		panic(fmt.Sprintf("uncatalogued error code: %s", code))
	}
	return entry
}

func Lookup(code Code) (Entry, bool) {
	entry, ok := catalogue[code]
	return entry, ok
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

func Codes() []Code {
	entries := Catalogue()
	out := make([]Code, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Code)
	}
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
	data, err := json.Marshal(ErrorPayload{Error: e.Object()})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func FromObject(obj ErrorObject) *Error {
	code := Code(obj.Code)
	MustLookup(code)
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

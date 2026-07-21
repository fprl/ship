package helper

import (
	"io"
	"os"
	"path/filepath"

	"github.com/fprl/ship/internal/deploybundle"
	"github.com/fprl/ship/internal/deployrequest"
	"github.com/fprl/ship/internal/host"
)

var runIngestApply = func(apply *appApplyCmd) error { return apply.runAuthorized() }

// appIngestCmd is the public deploy interface. It accepts one framed bundle
// on stdin and owns the private staging directory for the whole deployment.
type appIngestCmd struct {
	App           string    `arg:"" help:"App name."`
	Env           string    `arg:"" help:"Env name."`
	BundleSize    int64     `name:"bundle-size" required:"" help:"Exact deploy bundle size in bytes."`
	BundleSHA256  string    `name:"bundle-sha256" required:"" help:"SHA-256 digest of the deploy bundle."`
	SHA           string    `name:"sha" required:"" help:"Release identifier."`
	Dirty         bool      `name:"dirty" help:"Mark this release as built from a dirty worktree snapshot."`
	BaseCommit    string    `name:"base-commit" required:"" help:"Git commit the release is based on."`
	CreatedAt     string    `name:"created-at" required:"" help:"Release creation time in RFC3339."`
	Rebuild       bool      `name:"rebuild" help:"Pass --no-cache --pull=always to podman build."`
	Progress      bool      `name:"progress" hidden:"" help:"Emit structured deploy progress events."`
	Logs          bool      `name:"logs" hidden:"" help:"Emit build and release-command log events."`
	TLS           string    `name:"tls" enum:"auto,internal" default:"auto" hidden:"" help:"TLS mode stamped by the client for this deploy."`
	PreviewAlias  string    `name:"preview-alias" hidden:"" help:"Preview branch alias host derived by the client."`
	SSHKeyComment string    `name:"ssh-key-comment" help:"SSH public key comment for the deploying key."`
	GitAuthor     string    `name:"git-author" help:"Git author configured by the deploying client."`
	Input         io.Reader `kong:"-"`
}

func (c *appIngestCmd) Run() error {
	apply := c.applyCommand()
	if err := apply.validateRequest(); err != nil {
		return err
	}

	privateDir, err := os.MkdirTemp(host.DeployTmpDir(), "ingest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(privateDir)
	if err := os.Chmod(privateDir, 0700); err != nil {
		return err
	}
	emitter := newDeployProgressEmitter(c.Progress, c.Logs, os.Stderr)
	finishReceive := emitter.start("upload", "Upload")
	input := c.Input
	if input == nil {
		input = os.Stdin
	}
	err = deploybundle.Receive(input, deploybundle.Metadata{Size: c.BundleSize, SHA256: c.BundleSHA256}, privateDir)
	finishReceive(err)
	if err != nil {
		return err
	}
	apply.Tarball = filepath.Join(privateDir, deploybundle.SourceName)
	apply.Manifest = filepath.Join(privateDir, deploybundle.ManifestName)
	apply.PrivateDir = privateDir
	apply.ProgressOut = emitter
	// Authorization stays after transport validation, matching the old deploy
	// lifecycle: a failed upload must not consume a one-shot approval.
	if err := apply.authorize(); err != nil {
		return err
	}
	return runIngestApply(&apply)
}

func (c appIngestCmd) applyCommand() appApplyCmd {
	return appApplyCmd{Request: c.request()}
}

func (c appIngestCmd) request() deployrequest.Request {
	return deployrequest.Request{
		App: c.App, Env: c.Env,
		Bundle: deploybundle.Metadata{Size: c.BundleSize, SHA256: c.BundleSHA256},
		SHA:    c.SHA, Dirty: c.Dirty, BaseCommit: c.BaseCommit, CreatedAt: c.CreatedAt,
		Rebuild: c.Rebuild, Progress: c.Progress, Logs: c.Logs,
		TLS: c.TLS, PreviewAlias: c.PreviewAlias,
		Actor: deployrequest.Actor{SSHKeyComment: c.SSHKeyComment, GitAuthor: c.GitAuthor},
	}
}

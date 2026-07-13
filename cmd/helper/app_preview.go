package helper

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/names"
	"github.com/fprl/ship/internal/secrets"
	"github.com/fprl/ship/internal/utils"
)

const (
	productionEnvName = "prod"
	previewTTL        = 72 * time.Hour
	previewMapLock    = "preview-map"
	previewSuffixLen  = 4
)

var (
	previewSanitizedBranchRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,26}[a-z0-9])?$|^[a-z0-9]$`)
	previewSuffixRe          = regexp.MustCompile(`^[a-z0-9]{4}$`)
	newPreviewSuffix         = randomPreviewSuffix
)

type appPreviewCmd struct {
	ResolveOrCreate appPreviewResolveOrCreateCmd `cmd:"resolve-or-create" help:"Resolve or create the preview mapping for a raw branch."`
	Resolve         appPreviewResolveCmd         `cmd:"resolve" help:"Resolve an existing preview mapping for a raw branch."`
	Pin             appPreviewPinCmd             `cmd:"pin" help:"Pin a preview mapping."`
	Unpin           appPreviewUnpinCmd           `cmd:"unpin" help:"Unpin a preview mapping."`
	Share           appPreviewShareCmd           `cmd:"share" help:"Print or rotate this preview's capability URL."`
}

type appPreviewResolveOrCreateCmd struct {
	App    string `arg:"" help:"App name."`
	Branch string `arg:"" help:"Raw branch name."`
}

func (c appPreviewResolveOrCreateCmd) Run() error {
	if err := validatePreviewAppBranch(c.App, c.Branch); err != nil {
		utils.DieError(err, 1)
	}
	var env string
	withAppNamedLock(c.App, previewMapLock, func() {
		resolved, err := resolveOrCreatePreview(c.App, c.Branch, time.Now().UTC())
		if err != nil {
			utils.DieError(err, 1)
		}
		env = resolved
	})
	fmt.Println(env)
	return nil
}

type appPreviewResolveCmd struct {
	App    string `arg:"" help:"App name."`
	Branch string `arg:"" help:"Raw branch name."`
}

func (c appPreviewResolveCmd) Run() error {
	if err := validatePreviewAppBranch(c.App, c.Branch); err != nil {
		utils.DieError(err, 1)
	}
	authorizeOrDie(helperVerbRead, authTargetForPreviewBranch(c.App, c.Branch, "preview-resolve"))
	file, ok, err := findPreviewByBranch(c.App, c.Branch)
	if err != nil {
		utils.DieError(err, 1)
	}
	if !ok {
		utils.DieError(unknownPreviewBranchError(c.Branch), 1)
	}
	fmt.Println(file.Env)
	return nil
}

type appPreviewPinCmd struct {
	App    string `arg:"" help:"App name."`
	Branch string `arg:"" help:"Raw branch name."`
}

func (c appPreviewPinCmd) Run() error {
	if err := validatePreviewAppBranch(c.App, c.Branch); err != nil {
		utils.DieError(err, 1)
	}
	authorizeOrDie(helperVerbPreviewPin, authTargetForPreviewBranch(c.App, c.Branch, "pin"))
	file, ok, err := findPreviewByBranch(c.App, c.Branch)
	if err != nil {
		utils.DieError(err, 1)
	}
	if !ok {
		utils.DieError(unknownPreviewBranchError(c.Branch), 1)
	}
	withAppEnvLock(file.App, file.Env, func() {
		if err := pinPreview(file.App, file.Env, true); err != nil {
			utils.DieError(err, 1)
		}
	})
	fmt.Printf("Pinned %s (%s) for branch %s\n", file.App, file.Env, c.Branch)
	return nil
}

type appPreviewUnpinCmd struct {
	App    string `arg:"" help:"App name."`
	Branch string `arg:"" help:"Raw branch name."`
}

func (c appPreviewUnpinCmd) Run() error {
	if err := validatePreviewAppBranch(c.App, c.Branch); err != nil {
		utils.DieError(err, 1)
	}
	authorizeOrDie(helperVerbPreviewPin, authTargetForPreviewBranch(c.App, c.Branch, "unpin"))
	file, ok, err := findPreviewByBranch(c.App, c.Branch)
	if err != nil {
		utils.DieError(err, 1)
	}
	if !ok {
		utils.DieError(unknownPreviewBranchError(c.Branch), 1)
	}
	withAppEnvLock(file.App, file.Env, func() {
		if err := pinPreview(file.App, file.Env, false); err != nil {
			utils.DieError(err, 1)
		}
	})
	fmt.Printf("Unpinned %s (%s) for branch %s\n", file.App, file.Env, c.Branch)
	return nil
}

func validatePreviewAppBranch(app, branch string) error {
	if !names.AppRe.MatchString(app) {
		return fmt.Errorf("invalid app name: %q", app)
	}
	if !names.ValidGitBranch(branch) {
		return fmt.Errorf("invalid preview branch mapping key: %q", branch)
	}
	if names.SanitizeBranchEnvName(branch) == "" {
		return fmt.Errorf("branch %q does not produce a valid environment name", branch)
	}
	return nil
}

func resolveOrCreatePreview(app, branch string, now time.Time) (string, error) {
	if file, ok, err := findPreviewByBranch(app, branch); err != nil {
		return "", err
	} else if ok {
		if _, err := ensurePreviewCapability(app, file.Env); err != nil {
			return "", err
		}
		return file.Env, nil
	}

	base := names.SanitizeBranchEnvName(branch)
	for attempt := 0; attempt < 20; attempt++ {
		suffix, err := newPreviewSuffix()
		if err != nil {
			return "", err
		}
		env := base + "-" + suffix
		if err := validateAppEnv(app, env); err != nil {
			return "", err
		}
		if previewEnvExists(app, env) {
			continue
		}
		expires := now.Add(previewTTL)
		preview := &identity.PreviewIdentity{
			Branch:          branch,
			SanitizedBranch: base,
			Env:             env,
			Suffix:          suffix,
			LastShipAt:      now,
			ExpiresAt:       &expires,
			Pinned:          false,
		}
		lock, err := acquireAppEnvLock(app, env)
		if err != nil {
			return "", err
		}
		if err := setupEnv(app, env); err != nil {
			_ = lock.Release()
			return "", err
		}
		if err := writeEnvIdentityWithPreview(app, env, preview); err != nil {
			_ = lock.Release()
			return "", err
		}
		if _, err := ensurePreviewCapability(app, env); err != nil {
			_ = lock.Release()
			return "", err
		}
		if err := lock.Release(); err != nil {
			return "", fmt.Errorf("release lock for %s (%s): %v", app, env, err)
		}
		return env, nil
	}
	return "", fmt.Errorf("could not allocate a unique preview suffix for branch %s", branch)
}

func ensurePreviewCapability(app, env string) (string, error) {
	value, err := secrets.GetPreviewCapability(app, env)
	if err == nil {
		return string(value), nil
	}
	if !errors.Is(err, secrets.ErrNotFound) {
		return "", err
	}
	valueString, err := generatePreviewCredential(32)
	if err != nil {
		return "", err
	}
	if err := secrets.PutPreviewCapability(app, env, []byte(valueString)); err != nil {
		return "", err
	}
	return valueString, nil
}

func previewEnvExists(app, env string) bool {
	if _, err := os.Stat(identity.EnvRoot(app, env)); err == nil {
		return true
	}
	file, ok, err := findPreviewByEnv(app, env)
	return err == nil && ok && file.Env == env
}

func findPreviewByBranch(app, branch string) (identity.EnvIdentity, bool, error) {
	files, err := previewIdentities(app)
	if err != nil {
		return identity.EnvIdentity{}, false, err
	}
	for _, file := range files {
		if file.Preview != nil && file.Preview.Branch == branch {
			return file, true, nil
		}
	}
	return identity.EnvIdentity{}, false, nil
}

func findPreviewByEnv(app, env string) (identity.EnvIdentity, bool, error) {
	files, err := previewIdentities(app)
	if err != nil {
		return identity.EnvIdentity{}, false, err
	}
	for _, file := range files {
		if file.Env == env {
			return file, true, nil
		}
	}
	return identity.EnvIdentity{}, false, nil
}

func previewIdentities(app string) ([]identity.EnvIdentity, error) {
	paths, err := filepath.Glob(identityGlob())
	if err != nil {
		return nil, err
	}
	var out []identity.EnvIdentity
	for _, path := range paths {
		file, err := readEnvIdentityFile(path)
		if err != nil {
			return nil, err
		}
		if file.App != app || file.Preview == nil {
			continue
		}
		if err := validatePreviewIdentity(file); err != nil {
			return nil, fmt.Errorf("invalid preview identity %s: %v", path, err)
		}
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].App != out[j].App {
			return out[i].App < out[j].App
		}
		return out[i].Env < out[j].Env
	})
	return out, nil
}

func validatePreviewIdentity(file identity.EnvIdentity) error {
	if file.Preview == nil {
		return nil
	}
	if file.Env == productionEnvName {
		return fmt.Errorf("prod cannot carry preview metadata")
	}
	if file.Preview.Env != file.Env {
		return fmt.Errorf("preview env %q does not match top-level env %q", file.Preview.Env, file.Env)
	}
	if !names.ValidGitBranch(file.Preview.Branch) {
		return fmt.Errorf("invalid branch %q", file.Preview.Branch)
	}
	if !previewSanitizedBranchRe.MatchString(file.Preview.SanitizedBranch) {
		return fmt.Errorf("invalid sanitized branch %q", file.Preview.SanitizedBranch)
	}
	if file.Preview.SanitizedBranch != names.SanitizeBranchEnvName(file.Preview.Branch) {
		return fmt.Errorf("sanitized branch %q does not match branch %q", file.Preview.SanitizedBranch, file.Preview.Branch)
	}
	if !previewSuffixRe.MatchString(file.Preview.Suffix) {
		return fmt.Errorf("invalid suffix %q", file.Preview.Suffix)
	}
	if file.Preview.Env != file.Preview.SanitizedBranch+"-"+file.Preview.Suffix {
		return fmt.Errorf("preview env %q does not match sanitized branch + suffix", file.Preview.Env)
	}
	if file.Preview.ExpiresAt == nil && !file.Preview.Pinned {
		return fmt.Errorf("unpinned preview has no expiry")
	}
	return nil
}

func refreshPreviewShip(app, env string, now time.Time) error {
	file, err := readEnvIdentity(app, env)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if file.Preview == nil {
		return nil
	}
	file.Preview.LastShipAt = now
	if file.Preview.Pinned {
		file.Preview.ExpiresAt = nil
	} else {
		expires := now.Add(previewTTL)
		file.Preview.ExpiresAt = &expires
	}
	return writeEnvIdentityWithPreview(app, env, file.Preview)
}

func isPreviewEnv(app, env string) (bool, error) {
	file, err := readEnvIdentity(app, env)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return file.Preview != nil, nil
}

func pinPreview(app, env string, pinned bool) error {
	file, err := readEnvIdentity(app, env)
	if err != nil {
		return err
	}
	if file.Preview == nil {
		return fmt.Errorf("%s (%s) is not a preview environment", app, env)
	}
	file.Preview.Pinned = pinned
	if pinned {
		file.Preview.ExpiresAt = nil
	} else {
		expires := file.Preview.LastShipAt.Add(previewTTL)
		file.Preview.ExpiresAt = &expires
	}
	return writeEnvIdentityWithPreview(app, env, file.Preview)
}

func randomPreviewSuffix() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	var out [previewSuffixLen]byte
	max := big.NewInt(int64(len(alphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate preview suffix: %w", err)
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out[:]), nil
}

func unknownPreviewBranchError(branch string) error {
	return errcat.New(errcat.CodeUnknownPreviewBranch, errcat.Fields{
		"branch":  fmt.Sprintf("%q", branch),
		"command": "git checkout " + utils.ShellEscape(branch) + " && ship",
	})
}

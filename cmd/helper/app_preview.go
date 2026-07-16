package helper

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fprl/ship/internal/caddy"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/names"
	"github.com/fprl/ship/internal/secrets"
	"github.com/fprl/ship/internal/utils"
)

const (
	productionEnvName = names.ProductionEnvName
	previewTTL        = 72 * time.Hour
	previewMapLock    = "preview-map"
)

var (
	newPreviewSuffix  = randomPreviewSuffix
	readCaddyFragment = os.ReadFile
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
	authorizeOrDie(helperVerbShip, authTargetForPreviewBranch(c.App, c.Branch, "resolve-or-create"))
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
	authorizeOrDie(helperVerbRead, authTargetForPreviewBranch(c.App, c.Branch, "resolve"))
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
	if names.PreviewSanitizedBranch(branch) == "" {
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

	base := names.PreviewSanitizedBranch(branch)
	hostLock, err := acquirePreviewHostLabelLock()
	if err != nil {
		return "", err
	}
	defer func() { _ = hostLock.Release() }()
	if env, ok := incompletePreviewEnv(app, base); ok {
		expires := now.Add(previewTTL)
		preview := &identity.PreviewIdentity{Branch: branch, LastShipAt: now, ExpiresAt: &expires}
		if err := completePreviewAllocation(app, env, preview); err != nil {
			return "", err
		}
		return env, nil
	}
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
		inUse, err := synthesizedHostLabelExists(names.SynthesizedHostLabel(app, env))
		if err != nil {
			return "", err
		}
		if inUse {
			continue
		}
		expires := now.Add(previewTTL)
		preview := &identity.PreviewIdentity{
			Branch:     branch,
			LastShipAt: now,
			ExpiresAt:  &expires,
		}
		if err := completePreviewAllocation(app, env, preview); err != nil {
			return "", err
		}
		return env, nil
	}
	return "", fmt.Errorf("could not allocate a unique preview suffix for branch %s", branch)
}

func incompletePreviewEnv(app, base string) (string, bool) {
	paths := make([]string, 0)
	for _, pattern := range []string{
		filepath.Join(identity.AppsRoot(), app+"."+base+"-*"),
		filepath.Join(secrets.RootDir(), app, base+"-*"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return "", false
		}
		paths = append(paths, matches...)
	}
	seen := make(map[string]bool)
	for _, path := range paths {
		env := filepath.Base(path)
		if strings.HasPrefix(env, app+".") {
			env = strings.TrimPrefix(env, app+".")
		}
		if seen[env] {
			continue
		}
		seen[env] = true
		if _, err := os.Stat(identity.IdentityFile(app, env)); err == nil {
			continue
		}
		if !strings.HasPrefix(env, base+"-") {
			continue
		}
		if _, ok := names.PreviewSuffix(env); ok {
			return env, true
		}
	}
	return "", false
}

func completePreviewAllocation(app, env string, preview *identity.PreviewIdentity) error {
	lock, err := acquireAppEnvLock(app, env)
	if err != nil {
		return err
	}
	if err := setupEnv(identity.EnvIdentity{
		Version: 1,
		App:     app,
		Env:     env,
		InfraID: identity.InfraID(app, env),
		Preview: preview,
	}); err != nil {
		_ = lock.Release()
		return err
	}
	if err := lock.Release(); err != nil {
		return fmt.Errorf("release lock for %s (%s): %v", app, env, err)
	}
	return nil
}

func synthesizedHostLabelExists(label string) (bool, error) {
	_, found, err := synthesizedHostLabelOwner(label)
	return found, err
}

func synthesizedHostLabelOwner(label string) (appEnvStatus, bool, error) {
	envs, err := identityAppEnvs()
	if err != nil {
		return appEnvStatus{}, false, err
	}
	for _, env := range envs {
		if names.SynthesizedHostLabel(env.App, env.Env) == label {
			return env, true, nil
		}
	}
	return appEnvStatus{}, false, nil
}

type previewHostOwner struct {
	App  string
	Env  string
	Kind string
}

func (o previewHostOwner) String() string {
	return fmt.Sprintf("%s (%s) %s", o.App, o.Env, o.Kind)
}

// previewAliasForContext derives alias state from the active release envelope and
// identity instead of persisting a second copy of it. The canonical host in
// the route overlay supplies the sslip fallback base when [preview].base is
// omitted.
func previewAliasForContext(app, env string, ctx *config.AppContext) (string, bool) {
	if env == productionEnvName || !ctx.Preview.Aliases {
		return "", false
	}
	label := names.SynthesizedHostLabel(app, env) + "."
	for _, route := range ctx.Routes {
		if !strings.HasPrefix(route.Host, label) {
			continue
		}
		base := strings.TrimPrefix(route.Host, label)
		if base == "" {
			continue
		}
		return names.PreviewBranchSlug(env) + "." + base, true
	}
	return "", false
}

// previewAliasOwner is the box-global host authority. It sees configured
// routes and generated canonical hosts in each env's active release envelope,
// plus aliases that are actually rendered in Caddy fragments. The caller holds
// the existing preview host-label lock while checking and rendering.
func previewAliasOwner(host, currentApp, currentEnv string, incoming map[string]config.Route) (previewHostOwner, bool, error) {
	for _, route := range incoming {
		if route.Host == host {
			return previewHostOwner{App: currentApp, Env: currentEnv, Kind: "route"}, true, nil
		}
	}

	envs, err := identityAppEnvs()
	if err != nil {
		return previewHostOwner{}, false, err
	}
	for _, item := range envs {
		if item.App == currentApp && item.Env == currentEnv {
			continue
		}
		ctx, cleanup, err := loadActiveEnvelopeContext(item.App, item.Env)
		if errcat.Is(err, errcat.CodeNoDeploys) {
			continue
		}
		if err != nil {
			return previewHostOwner{}, false, fmt.Errorf("read active release for %s (%s): %w", item.App, item.Env, err)
		}
		defer cleanup()
		for _, route := range ctx.Routes {
			if route.Host == host {
				return previewHostOwner{App: item.App, Env: item.Env, Kind: "route"}, true, nil
			}
		}
		owns, err := caddyFragmentOwnsHost(item.App, item.Env, host)
		if err != nil {
			return previewHostOwner{}, false, err
		}
		if owns {
			return previewHostOwner{App: item.App, Env: item.Env, Kind: "preview alias"}, true, nil
		}
	}
	return previewHostOwner{}, false, nil
}

func caddyFragmentOwnsHost(app, env, host string) (bool, error) {
	data, err := readCaddyFragment(identity.CaddyFragmentFile(app, env))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read Caddy fragment for %s (%s): %w", app, env, err)
	}
	quotedHost, err := caddy.CaddyQuote(host)
	if err != nil {
		return false, err
	}
	return strings.Contains(string(data), quotedHost+" {\n"), nil
}

func addPreviewAliasRoutes(app, env, alias string, ctx *config.AppContext) error {
	if !config.ValidateHost(alias) {
		return fmt.Errorf("invalid preview alias host %q", alias)
	}
	canonicalPrefix := names.SynthesizedHostLabel(app, env) + "."
	aliases := make(map[string]config.Route)
	for key, route := range ctx.Routes {
		if !strings.HasPrefix(route.Host, canonicalPrefix) {
			continue
		}
		copy := route
		copy.Host = alias
		copy.StorageKey = key
		aliases[alias+copy.Path] = copy
	}
	if len(aliases) == 0 {
		return fmt.Errorf("preview alias %q has no canonical preview route", alias)
	}
	for name, route := range aliases {
		ctx.Routes[name] = route
	}
	return nil
}

// addConfiguredPreviewAlias keeps every render path (deploy, capability
// rotation, and rollback) on the same ownership authority. A conflict is a
// successful deploy without the optional alias; canonical routes stay intact.
func addConfiguredPreviewAlias(app, env string, ctx *config.AppContext) error {
	alias, ok := previewAliasForContext(app, env, ctx)
	if !ok {
		return nil
	}
	hostLock, err := acquirePreviewHostLabelLock()
	if err != nil {
		return err
	}
	defer func() { _ = hostLock.Release() }()
	owner, found, err := previewAliasOwner(alias, app, env, ctx.Routes)
	if err != nil {
		return err
	}
	if found {
		fmt.Fprintf(os.Stderr, "warning: preview alias %s for %s (%s) skipped; already owned by %s\n", alias, app, env, owner)
		return nil
	}
	return addPreviewAliasRoutes(app, env, alias, ctx)
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
		return fmt.Errorf("production cannot carry preview metadata")
	}
	if !names.ValidGitBranch(file.Preview.Branch) {
		return fmt.Errorf("invalid branch %q", file.Preview.Branch)
	}
	sanitizedBranch := names.PreviewSanitizedBranch(file.Preview.Branch)
	if !names.ValidPreviewSanitizedBranch(sanitizedBranch) {
		return fmt.Errorf("invalid sanitized branch %q", sanitizedBranch)
	}
	suffix, ok := names.PreviewSuffix(file.Env)
	if !ok {
		return fmt.Errorf("invalid suffix derived from env %q", file.Env)
	}
	if file.Env != sanitizedBranch+"-"+suffix {
		return fmt.Errorf("preview env %q does not match sanitized branch + suffix", file.Env)
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
	if names.PreviewPinned(file.Preview.ExpiresAt) {
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
	var out [names.PreviewSuffixLen]byte
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

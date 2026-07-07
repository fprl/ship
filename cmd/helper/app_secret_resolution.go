package helper

import (
	"errors"
	"fmt"
	"os"

	"github.com/fprl/simple-vps/internal/secrets"
)

const sharedPreviewSecretsEnvName = "preview"

type secretScopeInfo struct {
	Preview bool
	Branch  string
}

func resolveSecretValue(app, env, key string) ([]byte, error) {
	info, scopes, err := secretLookupScopes(app, env)
	if err != nil {
		return nil, err
	}
	for _, scopeEnv := range scopes {
		val, err := secrets.Get(app, scopeEnv, key)
		if err == nil {
			return val, nil
		}
		if errors.Is(err, secrets.ErrNotFound) {
			continue
		}
		return nil, fmt.Errorf("read secret %s: %w", key, err)
	}
	return nil, secretMissingError(key, info)
}

func secretLookupScopes(app, env string) (secretScopeInfo, []string, error) {
	file, err := readEnvIdentity(app, env)
	if err != nil && !os.IsNotExist(err) {
		return secretScopeInfo{}, nil, err
	}
	if err == nil && file.Preview != nil {
		return secretScopeInfo{Preview: true, Branch: file.Preview.Branch}, []string{env, sharedPreviewSecretsEnvName}, nil
	}
	return secretScopeInfo{}, []string{env}, nil
}

func secretMissingError(key string, info secretScopeInfo) error {
	if info.Preview {
		return fmt.Errorf("secret_missing: missing secret %s for Preview branch %q\nnext: ship secret set %s [--preview|--branch <name>]", key, info.Branch, key)
	}
	return fmt.Errorf("secret_missing: missing secret %s for Production\nnext: ship secret set %s", key, key)
}

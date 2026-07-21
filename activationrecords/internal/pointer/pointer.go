package pointer

import (
	"encoding/json"

	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/store"
)

func Path(app, env string) string { return identity.ActiveFile(app, env) }

func Publish(path string, value any, prepare func(string) error) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return store.AtomicWritePrepared(path, append(data, '\n'), 0644, prepare)
}

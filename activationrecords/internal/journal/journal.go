package journal

import (
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/kernel"
)

func DeployPath(app, env string) string { return identity.DeployJournalFile(app, env) }

func Append(path string, entry any) error { return kernel.AppendJournal(path, entry) }

func Read(path string, decode func([]byte) error) (bool, error) {
	return kernel.ReadJournal(path, decode)
}

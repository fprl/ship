package helper

import (
	"errors"
	"fmt"
	"time"

	"github.com/fprl/ship/internal/activation"
	"github.com/fprl/ship/internal/store"
)

// commitAndConverge publishes the activation pointer and brings runtime into
// line with it. The completion callback owns the operation-specific success
// journal and post-journal cleanup.
func commitAndConverge(app, env string, pointer activation.Pointer, addStale func([]string), complete func() error) (bool, error) {
	activeErr := writeActive(app, env, pointer)
	if activeErr != nil {
		var published store.PublishedWriteError
		if !errors.As(activeErr, &published) {
			return false, activeErr
		}
		converged, convergeErr := convergeActiveWithPointer(app, env, pointer)
		addStale(converged.StaleContainers)
		if convergeErr != nil {
			return true, committedDegradedError{Err: newDeployCommittedDegradedError(fmt.Errorf("active pointer published but durability is degraded: %v; convergence failed: %w", activeErr, convergeErr))}
		}
		return true, committedDegradedError{Err: newDeployCommittedDegradedError(fmt.Errorf("active pointer published but durability is degraded: %v", activeErr))}
	}

	converged, err := convergeActiveWithPointer(app, env, pointer)
	addStale(converged.StaleContainers)
	if err != nil {
		return true, newDeployCommittedUnconvergedError(err)
	}
	if err := refreshPreviewShip(app, env, time.Now().UTC()); err != nil {
		return true, committedDegradedError{Err: newDeployCommittedDegradedError(fmt.Errorf("activation converged but preview metadata refresh failed: %w", err))}
	}
	return true, complete()
}

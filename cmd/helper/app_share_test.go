package helper

import (
	"errors"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/errcat"
)

func TestPreviewCapabilityRotationErrorReportsRollbackFailure(t *testing.T) {
	err := previewCapabilityRotationError(errors.New("rerender rejected config"), errors.New("restore token failed"))
	if !errcat.Is(err, errcat.CodeOperationFailed) {
		t.Fatalf("rotation error = %v, want operation_failed", err)
	}
	for _, want := range []string{
		"rerender rejected config",
		"rollback failed: restore token failed",
		"preview capability state is ambiguous",
		"ship preview share --rotate",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("rotation error %q does not contain %q", err, want)
		}
	}
}

func TestPreviewCapabilityRotationErrorKeepsRerenderFailureAfterSuccessfulRollback(t *testing.T) {
	rerenderErr := errors.New("rerender rejected config")
	if got := previewCapabilityRotationError(rerenderErr, nil); got != rerenderErr {
		t.Fatalf("rotation error = %v, want original rerender error %v", got, rerenderErr)
	}
}

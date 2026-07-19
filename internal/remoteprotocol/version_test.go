package remoteprotocol

import (
	"testing"

	"github.com/fprl/ship/internal/errcat"
)

func TestRequireExactVersion(t *testing.T) {
	tests := []struct {
		name   string
		client string
		helper string
		code   errcat.Code
		next   string
	}{
		{name: "exact release", client: "v0.9.2", helper: "v0.9.2"},
		{name: "exact development build", client: "v0.9.2-3-gabc", helper: "v0.9.2-3-gabc"},
		{name: "helper behind", client: "v0.9.2", helper: "v0.9.1", code: errcat.CodeBoxHelperBehind, next: "ship box update 203.0.113.7"},
		{name: "client behind", client: "v0.9.1", helper: "v0.9.2", code: errcat.CodeClientBehindHelper, next: "curl -fsSL https://github.com/fprl/ship/releases/latest/download/install.sh | bash"},
		{name: "client version missing", helper: "v0.9.2", code: errcat.CodeClientBehindHelper, next: "curl -fsSL https://github.com/fprl/ship/releases/latest/download/install.sh | bash"},
		{name: "different development builds", client: "v0.9.2-3-gabc", helper: "v0.9.2-4-gdef", code: errcat.CodeBoxVersionAmbiguous, next: "ship box setup 203.0.113.7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RequireExactVersion(tt.client, tt.helper, "203.0.113.7")
			if tt.code == "" {
				if err != nil {
					t.Fatalf("RequireExactVersion() = %v", err)
				}
				return
			}
			coded, ok := errcat.As(err)
			if !ok || coded.Code() != tt.code {
				t.Fatalf("RequireExactVersion() = %v, want %s", err, tt.code)
			}
			if coded.Remediation() != tt.next {
				t.Fatalf("remediation = %q, want %q", coded.Remediation(), tt.next)
			}
		})
	}
}

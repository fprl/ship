package remoteprotocol

import (
	"reflect"
	"strings"
	"testing"
)

func TestClientArgsRoundTrip(t *testing.T) {
	args := ClientArgs("v0.9.2", "app", "apply", "api", "production")
	want := []string{"--client-version", "v0.9.2", "app", "apply", "api", "production"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("ClientArgs() = %v, want %v", args, want)
	}
	invocation, err := ParseClientArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.ClientVersion != "v0.9.2" || invocation.Namespace != "app" || invocation.NamespaceIndex != 2 {
		t.Fatalf("invocation = %+v", invocation)
	}
}

func TestParseClientArgsRejectsMissingHeader(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"app", "apply"},
		{"--client-version", "", "app"},
		{"--client-version", "v0.9.2"},
	} {
		if _, err := ParseClientArgs(args); err == nil {
			t.Fatalf("ParseClientArgs(%v) succeeded", args)
		}
	}
}

func TestNamespaceClassification(t *testing.T) {
	if !ClientNamespaceAllowed("app") || !ClientNamespaceAllowed("gc") || ClientNamespaceAllowed("env") || ClientNamespaceAllowed("version") {
		t.Fatal("client namespace classification drifted")
	}
	if !RepairNamespaceAllowed("version") || !RepairNamespaceAllowed("update") || RepairNamespaceAllowed("app") {
		t.Fatal("repair namespace classification drifted")
	}
}

func TestSudoersLineAndValidationShareNamespacePolicy(t *testing.T) {
	line := SudoersLine("deploy")
	for _, namespace := range []string{"app", "doctor", "gc", "key"} {
		if !strings.Contains(line, "--client-version * "+namespace) {
			t.Fatalf("sudoers line missing %s: %s", namespace, line)
		}
	}
	if strings.Contains(line, "--internal") || strings.Contains(line, "--client-version * env") {
		t.Fatalf("sudoers line grants box-local commands: %s", line)
	}
	if !SudoersLineRegexp().MatchString(strings.TrimSuffix(line, "\n")) {
		t.Fatalf("generated sudoers line does not match validator: %s", line)
	}
}

func TestCatalogueClassifiesCompletePathsAndExposure(t *testing.T) {
	tests := []struct {
		args     []string
		exposure Exposure
	}{
		{ClientArgs("v1", "app", "preview", "resolve", "api", "branch"), ExposureClient},
		{ClientArgs("v1", "webhook", "set", "https://example.com"), ExposureClient},
		{[]string{"version", "--json"}, ExposureRepair},
		{[]string{"--internal", "doctor", "record"}, ExposureInternal},
		{[]string{"agent-shell", "--member-fingerprint", "SHA256:key"}, ExposureGateway},
	}
	for _, tt := range tests {
		invocation, err := Parse(tt.args)
		if err != nil {
			t.Fatalf("Parse(%v): %v", tt.args, err)
		}
		if invocation.Exposure != tt.exposure {
			t.Fatalf("Parse(%v) exposure = %v", tt.args, invocation.Exposure)
		}
	}
	for _, args := range [][]string{
		ClientArgs("v1", "app", "removed-verb"),
		{"--internal", "app", "ls"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("Parse(%v) allowed an invalid exposure/path", args)
		}
	}
}

func TestBindMemberReplacesEveryUntrustedClaim(t *testing.T) {
	invocation, err := Parse(ClientArgs("v1", "app", "--member-fingerprint=SHA256:lie", "status", "api", "production", "--member-fingerprint", "SHA256:other"))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindMember(invocation, " SHA256:pinned ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--client-version", "v1", "app", "--member-fingerprint", "SHA256:pinned", "status", "api", "production"}
	if !reflect.DeepEqual(bound.Args, want) {
		t.Fatalf("bound args = %#v, want %#v", bound.Args, want)
	}
}

func TestShellFieldsPreserveEmptyAndQuotedArguments(t *testing.T) {
	args := []string{"sudo", "", "a b", "it's", "plain"}
	parsed, err := ParseShellFields(RenderShellFields(args))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parsed, args) {
		t.Fatalf("round trip = %#v, want %#v", parsed, args)
	}
}

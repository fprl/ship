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

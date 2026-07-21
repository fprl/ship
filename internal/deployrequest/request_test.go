package deployrequest

import (
	"reflect"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/deploybundle"
)

func validRequest() Request {
	return Request{App: "api", Env: "production", Bundle: deploybundle.Metadata{Size: 10, SHA256: strings.Repeat("a", 64)}, SHA: "abc1234", BaseCommit: "abc1234", CreatedAt: "2026-07-19T10:00:00Z", Progress: true, TLS: "auto", Actor: Actor{SSHKeyComment: "key", GitAuthor: "Franco"}}
}

func TestRequestValidatesSemanticMetadataAndRendersCanonicalArgs(t *testing.T) {
	request := validRequest()
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	want := []string{"app", "apply", "--progress", "--tls", "auto", "--bundle-size", "10", "--bundle-sha256", strings.Repeat("a", 64), "--sha", "abc1234", "--base-commit", "abc1234", "--created-at", "2026-07-19T10:00:00Z", "--ssh-key-comment", "key", "--git-author", "Franco", "api", "production"}
	if got := request.CommandArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestRequestRejectsInvalidBundleAndReleaseProvenance(t *testing.T) {
	for _, mutate := range []func(*Request){
		func(r *Request) { r.Bundle.Size = 0 },
		func(r *Request) { r.SHA = "other123" },
		func(r *Request) { r.TLS = "whatever" },
	} {
		request := validRequest()
		mutate(&request)
		if err := request.Validate(); err == nil {
			t.Fatalf("request unexpectedly valid: %+v", request)
		}
	}
}

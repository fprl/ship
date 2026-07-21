package podmanruntime

import (
	"reflect"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/identity"
)

func TestContainerInventoryNormalizesImageIDs(t *testing.T) {
	var calls [][]string
	runtime := New(func(args []string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "ps":
			return []byte(`[{"Names":["ship-api-production-web"],"State":"running","Labels":{"ship.app":"api","ship.env":"production"}}]`), nil
		case "inspect":
			return []byte(`[{"Name":"/ship-api-production-web","Image":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]`), nil
		}
		return nil, nil
	})
	containers, err := runtime.Containers("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := runtime.ContainerImageIDs(containers)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids["ship-api-production-web"]; got != strings.Repeat("a", 64) {
		t.Fatalf("image ID = %q", got)
	}
	if len(calls) != 2 || !reflect.DeepEqual(calls[0], []string{"ps", "-a", "--filter", "label=ship.app=api", "--filter", "label=ship.env=production", "--format", "json"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestRuntimeSecurityFloorIsNotCallerOptional(t *testing.T) {
	args := BaseRunArgs(ContainerSpec{App: "api", Env: "production", Process: "web", UserID: "1001", GroupID: "1001", Release: "abc1234", Networks: []string{"app", "ingress"}})
	args = WithReadOnlyRoot(args)
	joined := strings.Join(args, " ")
	for _, required := range []string{"--user 1001:1001", "--cap-drop ALL", "--security-opt no-new-privileges", "--pids-limit 512", "/data:Z", "--read-only", "/tmp:size=64m,mode=1777"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("runtime args missing %q: %s", required, joined)
		}
	}
}

func TestImagesGroupPhysicalIDsAndCollectShipTags(t *testing.T) {
	entries := []ImageEntry{
		{ID: "sha256:" + strings.Repeat("b", 64), Names: []string{"localhost/" + identity.ImageRepo("api", "production") + ":one"}, Labels: map[string]string{"ship.app": "api", "ship.env": "production", "ship.release": "abc1234"}},
		{ID: strings.Repeat("b", 64), RepoTags: []string{identity.ImageRepo("api", "production") + ":two"}, Labels: map[string]string{"ship.app": "api", "ship.env": "production", "ship.release": "abc1234"}},
	}
	images := ImagesFromEntries("api", "production", entries)
	if len(images) != 1 || images[0].ImageID != strings.Repeat("b", 64) || len(images[0].ShipTags) != 2 {
		t.Fatalf("images = %+v", images)
	}
}

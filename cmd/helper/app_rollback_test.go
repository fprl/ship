package helper

import (
	"strings"
	"testing"
)

func TestCurrentReleaseRejectsEmptyOrMixedProcesses(t *testing.T) {
	if _, err := currentRelease(nil); err == nil || !strings.Contains(err.Error(), "no processes running") {
		t.Fatalf("expected empty-processes error, got %v", err)
	}
	_, err := currentRelease([]processStatus{
		{Process: "web", Release: "aaa"},
		{Process: "worker", Release: "bbb"},
	})
	if err == nil || !strings.Contains(err.Error(), "different releases") {
		t.Fatalf("expected mixed-release error, got %v", err)
	}
}

func TestSelectRollbackRelease(t *testing.T) {
	images := []imageRelease{
		{Release: "3333333"},
		{Release: "2222222"},
		{Release: "1111111"},
	}
	got, err := selectRollbackRelease(images, "3333333", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Release != "2222222" {
		t.Fatalf("expected previous release 2222222, got %+v", got)
	}

	got, err = selectRollbackRelease(images, "3333333", "1111111")
	if err != nil {
		t.Fatal(err)
	}
	if got.Release != "1111111" {
		t.Fatalf("expected requested release 1111111, got %+v", got)
	}
}

func TestSelectRollbackReleaseErrors(t *testing.T) {
	_, err := selectRollbackRelease([]imageRelease{{Release: "3333333"}}, "3333333", "")
	if err == nil || !strings.Contains(err.Error(), "no previous release") {
		t.Fatalf("expected no previous release error, got %v", err)
	}
	_, err = selectRollbackRelease([]imageRelease{{Release: "3333333"}}, "3333333", "2222222")
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("expected missing release error, got %v", err)
	}
	_, err = selectRollbackRelease([]imageRelease{{Release: "3333333"}}, "3333333", "3333333")
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected already running error, got %v", err)
	}
}

func TestImageReleasesFromEntriesUsesPodmanLabels(t *testing.T) {
	entries := []imageEntry{
		{
			Names: []string{"localhost/simple-vps/svps-de70a215abfd:3333333"},
			Labels: map[string]string{
				"simple-vps.app":      "hello",
				"simple-vps.env":      "production",
				"simple-vps.infra_id": "svps-de70a215abfd",
				"simple-vps.release":  "3333333",
			},
		},
		{
			Names: []string{"localhost/simple-vps/svps-de70a215abfd:2222222"},
			Labels: map[string]string{
				"simple-vps.app":      "hello",
				"simple-vps.env":      "production",
				"simple-vps.infra_id": "svps-de70a215abfd",
				"simple-vps.release":  "2222222",
			},
		},
		{
			Names: []string{"localhost/simple-vps/svps-other:ignored"},
			Labels: map[string]string{
				"simple-vps.app":      "hello",
				"simple-vps.env":      "production",
				"simple-vps.infra_id": "svps-other",
				"simple-vps.release":  "ignored",
			},
		},
		{
			Names: []string{"localhost/simple-vps/svps-de70a215abfd:1111111"},
			Tag:   "1111111",
			Labels: map[string]string{
				"simple-vps.app":      "hello",
				"simple-vps.env":      "production",
				"simple-vps.infra_id": "svps-de70a215abfd",
			},
		},
	}

	got := imageReleasesFromEntries("hello", "production", entries)
	if len(got) != 2 || got[0].Release != "3333333" || got[1].Release != "2222222" {
		t.Fatalf("unexpected releases: %+v", got)
	}
}

func TestRenderRollbackText(t *testing.T) {
	out := renderRollbackText(rollbackPayload{
		App:       "api",
		Env:       "production",
		Previous:  "3333333",
		Release:   "2222222",
		Processes: []string{"web"},
	})
	if !strings.Contains(out, "Rolled back api (production) from 3333333 to 2222222") {
		t.Fatalf("missing rollback summary:\n%s", out)
	}
	if !strings.Contains(out, "web") || !strings.Contains(out, "running") {
		t.Fatalf("missing process row:\n%s", out)
	}
}

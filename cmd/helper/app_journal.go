package helper

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/utils"
)

const deployJournalSchemaVersion = 1

type deployIdentity struct {
	SSHKeyComment string `json:"ssh_key_comment"`
	GitAuthor     string `json:"git_author"`
}

type journalMember struct {
	Fingerprint string `json:"fingerprint,omitempty"`
	Name        string `json:"name"`
	Role        string `json:"role"`
}

func deployActor(sshKeyComment, gitAuthor string) deployIdentity {
	actor := deployIdentity{SSHKeyComment: sshKeyComment, GitAuthor: gitAuthor}
	if actor.SSHKeyComment == "" {
		actor.SSHKeyComment = "unknown"
	}
	if actor.GitAuthor == "" {
		actor.GitAuthor = "unknown"
	}
	return actor
}

type journalProbe struct {
	Status      int    `json:"status"`
	BodySnippet string `json:"body_snippet"`
}

type deployJournalEntry struct {
	SchemaVersion    int            `json:"schema_version"`
	App              string         `json:"app"`
	Env              string         `json:"env"`
	Outcome          string         `json:"outcome"`
	StartedAt        string         `json:"started_at"`
	EndedAt          string         `json:"ended_at"`
	PreviousRelease  string         `json:"previous_release"`
	AttemptedRelease string         `json:"attempted_release"`
	FailingStep      string         `json:"failing_step"`
	StderrTail       string         `json:"stderr_tail"`
	ImagePrune       string         `json:"image_prune,omitempty"`
	Identity         deployIdentity `json:"identity"`
	Member           *journalMember `json:"member,omitempty"`
	Probe            *journalProbe  `json:"probe"`
}

type journalStepError struct {
	Step        string
	Err         error
	StderrTail  string
	Probe       *journalProbe
	ScrubValues []string
}

func (e *journalStepError) Error() string {
	return e.Err.Error()
}

func (e *journalStepError) Unwrap() error {
	return e.Err
}

func newJournalStepError(step string, err error, scrubValues []string, probe *journalProbe) error {
	if err == nil {
		return nil
	}
	tail := commandErrorTail(err)
	if tail == "" && probe != nil && probe.BodySnippet != "" {
		tail = probe.BodySnippet
	}
	return &journalStepError{
		Step:        step,
		Err:         err,
		StderrTail:  tail,
		Probe:       probe,
		ScrubValues: append([]string(nil), scrubValues...),
	}
}

func sanitizeDeployJournalEntry(app, env string, entry deployJournalEntry, scrubValues []string) deployJournalEntry {
	entry.SchemaVersion = deployJournalSchemaVersion
	entry.App = app
	entry.Env = env
	entry.StderrTail = scrubText(tailLines(entry.StderrTail, 40), scrubValues)
	if entry.Probe != nil {
		entry.Probe.BodySnippet = scrubText(tailLines(entry.Probe.BodySnippet, 8), scrubValues)
	}
	return entry
}

func appendDeployJournalEntry(app, env string, entry deployJournalEntry, scrubValues []string) error {
	entry = sanitizeDeployJournalEntry(app, env, entry, scrubValues)
	return appendSanitizedDeployJournalEntry(app, env, entry)
}

func appendSanitizedDeployJournalEntry(app, env string, entry deployJournalEntry) error {
	if err := validateAppEnv(app, env); err != nil {
		return err
	}
	path := identity.DeployJournalFile(app, env)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir deploy journal dir: %v", err)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open deploy journal: %v", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write deploy journal: %v", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close deploy journal: %v", err)
	}
	if _, err := utils.RunChecked("chown", []string{"root:root", path}, ""); err != nil {
		return fmt.Errorf("chown deploy journal: %v", err)
	}
	return nil
}

func readLatestDeployJournalEntry(app, env string) (deployJournalEntry, error) {
	entries, err := readDeployJournalEntries(app, env)
	if err != nil {
		return deployJournalEntry{}, err
	}
	if len(entries) == 0 {
		return deployJournalEntry{}, noDeployJournalError(app, env)
	}
	return entries[len(entries)-1], nil
}

func readLatestSuccessfulDeployJournalEntry(app, env string) (deployJournalEntry, error) {
	entries, err := readDeployJournalEntries(app, env)
	if err != nil {
		return deployJournalEntry{}, err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Outcome == "deployed" || entries[i].Outcome == "rolled_back" {
			return entries[i], nil
		}
	}
	return deployJournalEntry{}, noDeployJournalError(app, env)
}

func readDeployJournalEntries(app, env string) ([]deployJournalEntry, error) {
	if err := validateAppEnv(app, env); err != nil {
		return nil, err
	}
	path := identity.DeployJournalFile(app, env)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, noDeployJournalError(app, env)
		}
		return nil, fmt.Errorf("read deploy journal %s: %v", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var entries []deployJournalEntry
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry deployJournalEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("parse deploy journal %s: %v", path, err)
		}
		if entry.SchemaVersion != deployJournalSchemaVersion {
			return nil, fmt.Errorf("unsupported deploy journal schema version %d", entry.SchemaVersion)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read deploy journal %s: %v", path, err)
	}
	return entries, nil
}

func noDeployJournalError(app, env string) error {
	return errcat.New(errcat.CodeNoDeploys, errcat.Fields{
		"app": app,
		"env": env,
	})
}

func deployJournalFailureEntry(app, env, previousRelease, attemptedRelease string, actor deployIdentity, startedAt time.Time, err error) (deployJournalEntry, []string) {
	step := "release"
	tail := commandErrorTail(err)
	var probe *journalProbe
	var scrubValues []string
	var stepErr *journalStepError
	if errors.As(err, &stepErr) {
		if stepErr.Step != "" {
			step = stepErr.Step
		}
		if stepErr.StderrTail != "" {
			tail = stepErr.StderrTail
		}
		probe = stepErr.Probe
		scrubValues = append(scrubValues, stepErr.ScrubValues...)
	}
	if tail == "" {
		tail = err.Error()
	}
	return deployJournalEntry{
		SchemaVersion:    deployJournalSchemaVersion,
		App:              app,
		Env:              env,
		Outcome:          outcomeForFailingStep(step),
		StartedAt:        startedAt.Format(time.RFC3339Nano),
		EndedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		PreviousRelease:  previousRelease,
		AttemptedRelease: attemptedRelease,
		FailingStep:      step,
		StderrTail:       tail,
		Identity:         actor,
		Member:           currentServerMemberForJournal(),
		Probe:            probe,
	}, scrubValues
}

func outcomeForFailingStep(step string) string {
	switch step {
	case "build":
		return "aborted_build"
	case "probe":
		return "aborted_probe"
	default:
		return "aborted_release"
	}
}

func commandErrorTail(err error) string {
	var cmdErr *utils.CommandError
	if !errors.As(err, &cmdErr) {
		return ""
	}
	return tailLines(cmdErr.CombinedOutput(), 40)
}

func collectEnvValues(vals map[string]string) []string {
	out := make([]string, 0, len(vals))
	seen := map[string]bool{}
	for _, value := range vals {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func scrubText(text string, values []string) string {
	out := text
	for _, value := range values {
		if value == "" {
			continue
		}
		out = strings.ReplaceAll(out, value, "[redacted]")
	}
	return out
}

func tailLines(text string, n int) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimRight(text, "\n")
	if text == "" || n <= 0 {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func currentActiveReleaseBestEffort(app, env string) string {
	ctx, cleanup, err := loadAppliedAppContext(app, env)
	if err == nil {
		defer cleanup()
		if release, err := activeRelease(app, env, ctx); err == nil {
			return release
		}
	}
	containers, err := podmanPSContainers(app, env)
	if err == nil {
		if release, err := currentRelease(runningProcesses(containersToProcesses(containers))); err == nil {
			return release
		}
	}
	if release, err := currentStaticRelease(app, env); err == nil {
		return release
	}
	return ""
}

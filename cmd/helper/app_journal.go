package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fprl/ship/internal/artifact"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/journal"
	"github.com/fprl/ship/internal/utils"
)

const deployJournalSchemaVersion = 2

const tornDeployJournalWarning = "warning: deploy journal has an incomplete final entry (interrupted write); next: ship box doctor"

func warnTornDeployJournal(path string) {
	fmt.Fprintln(os.Stderr, tornDeployJournalWarning)
}

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
	SchemaVersion    int             `json:"schema_version"`
	App              string          `json:"app"`
	Env              string          `json:"env"`
	Outcome          string          `json:"outcome"`
	StartedAt        string          `json:"started_at"`
	EndedAt          string          `json:"ended_at"`
	PreviousRelease  string          `json:"previous_release"`
	AttemptedRelease string          `json:"attempted_release"`
	Activation       string          `json:"activation,omitempty"`
	Artifact         *artifact.Tuple `json:"artifact,omitempty"`
	FailingStep      string          `json:"failing_step"`
	StderrTail       string          `json:"stderr_tail"`
	GC               string          `json:"gc,omitempty"`
	Identity         deployIdentity  `json:"identity"`
	Member           *journalMember  `json:"member,omitempty"`
	Probe            *journalProbe   `json:"probe"`
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
	if err := journal.Append(path, entry); err != nil {
		return fmt.Errorf("append deploy journal: %w", err)
	}
	return nil
}

func resetLegacyDeployJournalForV2(app, env string) error {
	path := identity.LegacyDeployJournalFile(app, env)
	if err := os.Remove(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	fmt.Printf("Deleted v1 deploy journal; starting fresh v2 history\n")
	return nil
}

func readLatestDeployJournalEntry(app, env string) (deployJournalEntry, error) {
	entry, _, err := readLatestDeployJournalEntryWithStatus(app, env)
	return entry, err
}

func readLatestDeployJournalEntryWithStatus(app, env string) (deployJournalEntry, bool, error) {
	entries, torn, err := readDeployJournalEntriesWithStatus(app, env)
	if err != nil {
		return deployJournalEntry{}, torn, err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Outcome == "gc" {
			continue
		}
		return entries[i], torn, nil
	}
	return deployJournalEntry{}, torn, noDeployJournalError(app, env)
}

func readLatestSuccessfulDeployJournalEntry(app, env string) (deployJournalEntry, error) {
	entry, _, err := readLatestSuccessfulDeployJournalEntryWithStatus(app, env)
	return entry, err
}

func readLatestSuccessfulDeployJournalEntryWithStatus(app, env string) (deployJournalEntry, bool, error) {
	entries, torn, err := readDeployJournalEntriesWithStatus(app, env)
	if err != nil {
		return deployJournalEntry{}, torn, err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Outcome == "deployed" || entries[i].Outcome == "rolled_back" {
			return entries[i], torn, nil
		}
	}
	return deployJournalEntry{}, torn, noDeployJournalError(app, env)
}

func readDeployJournalEntries(app, env string) ([]deployJournalEntry, error) {
	entries, _, err := readDeployJournalEntriesWithStatus(app, env)
	if err == nil && len(entries) == 0 {
		return nil, noDeployJournalError(app, env)
	}
	return entries, err
}

func readDeployJournalEntriesWithStatus(app, env string) ([]deployJournalEntry, bool, error) {
	if err := validateAppEnv(app, env); err != nil {
		return nil, false, err
	}
	path := identity.DeployJournalFile(app, env)
	if _, readErr := os.Stat(path); readErr != nil && !os.IsNotExist(readErr) {
		return nil, false, fmt.Errorf("read deploy journal %s: %w", path, readErr)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, false, noDeployJournalError(app, env)
		}
		return nil, false, fmt.Errorf("stat deploy journal %s: %w", path, err)
	}
	var entries []deployJournalEntry
	torn, err := journal.Read(path, func(line []byte) error {
		var entry deployJournalEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return err
		}
		if entry.SchemaVersion != deployJournalSchemaVersion {
			return fmt.Errorf("unsupported deploy journal schema version %d", entry.SchemaVersion)
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, torn, fmt.Errorf("read deploy journal %s: %w", path, err)
	}
	return entries, torn, nil
}

func noDeployJournalError(app, env string) error {
	return errcat.New(errcat.CodeNoDeploys, errcat.Fields{
		"app": app,
		"env": env,
	})
}

func deployJournalFailureEntry(app, env, previousRelease, attemptedRelease string, actor deployIdentity, startedAt time.Time, err error) (deployJournalEntry, []string) {
	step := "apply"
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
		Outcome:          "failed",
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

func committedOutcomeJournalEntry(app, env, outcome, previousRelease, attemptedRelease string, actor deployIdentity, startedAt time.Time, failingStep string, artifact *artifact.Tuple, err error) (deployJournalEntry, []string) {
	stepErr := newJournalStepError(failingStep, err, nil, nil)
	entry, scrubValues := deployJournalFailureEntry(app, env, previousRelease, attemptedRelease, actor, startedAt, stepErr)
	entry.Outcome = outcome
	entry.Artifact = artifact
	return entry, scrubValues
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

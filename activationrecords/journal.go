package activationrecords

import (
	"encoding/json"
	"fmt"
	"strings"

	journalformat "github.com/fprl/ship/activationrecords/internal/journal"
)

const DeployJournalSchemaVersion = 2

type Outcome string

const (
	Converged            Outcome = "converged"
	Deployed             Outcome = "deployed"
	RolledBack           Outcome = "rolled_back"
	CommittedUnconverged Outcome = "committed_unconverged"
	CommittedDegraded    Outcome = "committed_degraded"
	Failed               Outcome = "failed"
	GC                   Outcome = "gc"
)

func (o Outcome) FailedBeforeCommit() bool { return o == Failed }

func (o Outcome) RetainsArtifact() bool {
	switch o {
	case Deployed, RolledBack, CommittedUnconverged, CommittedDegraded:
		return true
	default:
		return false
	}
}

func (o Outcome) CompletedLifecycle() bool { return o == Deployed || o == RolledBack }

// ValidOutcome is the closed outcome vocabulary owned by this package.
func ValidOutcome(o Outcome) bool {
	switch o {
	case Converged, Deployed, RolledBack, CommittedUnconverged, CommittedDegraded, Failed, GC:
		return true
	default:
		return false
	}
}

type Identity struct {
	SSHKeyComment string `json:"ssh_key_comment"`
	GitAuthor     string `json:"git_author"`
}

type Member struct {
	Fingerprint string `json:"fingerprint,omitempty"`
	Name        string `json:"name"`
	Role        string `json:"role"`
}

type Probe struct {
	Status      int    `json:"status"`
	BodySnippet string `json:"body_snippet"`
}

type JournalEntry struct {
	SchemaVersion    int      `json:"schema_version"`
	App              string   `json:"app"`
	Env              string   `json:"env"`
	Outcome          Outcome  `json:"outcome"`
	StartedAt        string   `json:"started_at"`
	EndedAt          string   `json:"ended_at"`
	PreviousRelease  string   `json:"previous_release"`
	AttemptedRelease string   `json:"attempted_release"`
	Activation       string   `json:"activation,omitempty"`
	Artifact         *Tuple   `json:"artifact,omitempty"`
	FailingStep      string   `json:"failing_step"`
	StderrTail       string   `json:"stderr_tail"`
	GC               string   `json:"gc,omitempty"`
	Identity         Identity `json:"identity"`
	Member           *Member  `json:"member,omitempty"`
	Probe            *Probe   `json:"probe"`
}

// NormalizeDeployJournalEntry applies the deploy journal's persisted-record
// normalization without writing it.
func NormalizeDeployJournalEntry(app, env string, entry JournalEntry, scrubValues []string) JournalEntry {
	entry.SchemaVersion = DeployJournalSchemaVersion
	entry.App = app
	entry.Env = env
	entry.StderrTail = scrubText(tailLines(entry.StderrTail, 40), scrubValues)
	if entry.Probe != nil {
		entry.Probe.BodySnippet = scrubText(tailLines(entry.Probe.BodySnippet, 8), scrubValues)
	}
	return entry
}

// AppendDeployJournal publishes one sanitized deploy record durably.
func AppendDeployJournal(app, env string, entry JournalEntry, scrubValues []string) error {
	if !ValidOutcome(entry.Outcome) {
		return fmt.Errorf("unsupported deploy journal outcome %q", entry.Outcome)
	}
	entry = NormalizeDeployJournalEntry(app, env, entry, scrubValues)
	return journalformat.Append(journalformat.DeployPath(app, env), entry)
}

func ReadDeployJournal(app, env string) ([]JournalEntry, bool, error) {
	path := journalformat.DeployPath(app, env)
	var entries []JournalEntry
	torn, err := journalformat.Read(path, func(line []byte) error {
		var entry JournalEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return err
		}
		if entry.SchemaVersion != DeployJournalSchemaVersion {
			return fmt.Errorf("unsupported deploy journal schema version %d", entry.SchemaVersion)
		}
		if !ValidOutcome(entry.Outcome) {
			return fmt.Errorf("unsupported deploy journal outcome %q", entry.Outcome)
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, torn, fmt.Errorf("read deploy journal %s: %w", path, err)
	}
	return entries, torn, nil
}

// CommittedHistory returns every distinct committed artifact in newest-first
// order. It never applies retention; candidate policy does that after trust
// verification.
func CommittedHistory(app, env string, pointer Pointer) ([]Tuple, bool, error) {
	entries, torn, err := ReadDeployJournal(app, env)
	if err != nil {
		return nil, torn, err
	}
	seen := map[Tuple]bool{}
	history := make([]Tuple, 0, len(entries)+1)
	appendTuple := func(tuple Tuple) {
		if tuple.Release == "" || seen[tuple] {
			return
		}
		seen[tuple] = true
		history = append(history, tuple)
	}
	appendTuple(pointer.Artifact)
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Artifact == nil || !entries[i].Outcome.RetainsArtifact() {
			continue
		}
		appendTuple(*entries[i].Artifact)
	}
	return history, torn, nil
}

func tailLines(value string, n int) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.TrimRight(value, "\n")
	if value == "" || n <= 0 {
		return ""
	}
	lines := strings.Split(value, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func scrubText(value string, values []string) string {
	for _, item := range values {
		if item != "" {
			value = strings.ReplaceAll(value, item, "[redacted]")
		}
	}
	return value
}

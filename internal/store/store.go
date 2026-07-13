package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Store struct {
	Root string
}
type hostFileRaw struct {
	Version  int             `json:"version"`
	Desired  json.RawMessage `json:"desired"`
	Observed HostObserved    `json:"observed"`
	Meta     HostMeta        `json:"meta"`
}

func DefaultRoot() string {
	if root := os.Getenv("SHIP_STATE_DIR"); root != "" {
		return root
	}
	return "/etc/ship"
}

func Default() Store {
	return Store{Root: DefaultRoot()}
}

func (s Store) root() string {
	if s.Root != "" {
		return s.Root
	}
	return DefaultRoot()
}

func (s Store) HostPath() string {
	return filepath.Join(s.root(), "host.json")
}

func (s Store) DoctorPath() string {
	return filepath.Join(s.root(), "doctor.json")
}

func (s Store) MembersPath() string {
	return filepath.Join(s.root(), "members.json")
}

func (s Store) BoxConfigPath() string {
	return filepath.Join(s.root(), "box-config.json")
}

func (s Store) ApprovalsPath() string {
	return filepath.Join(s.root(), "approvals.json")
}

func (s Store) ApprovalsJournalPath() string {
	return filepath.Join(s.root(), "approvals-journal.jsonl")
}

func (s Store) UpdatesJournalPath() string {
	return filepath.Join(s.root(), "updates-journal.jsonl")
}

func (s Store) HostInstalled() (bool, error) {
	if _, err := os.Stat(s.HostPath()); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ReadHost returns os.IsNotExist(err) when host.json has not been created yet.
func (s Store) ReadHost() (*HostFile, error) {
	var file HostFile
	if err := readJSON(s.HostPath(), &file); err != nil {
		return nil, err
	}
	if err := validateVersion("host.json", file.Version); err != nil {
		return nil, err
	}
	normalizeHostFile(&file)
	if err := validateHostDesired(file.Desired); err != nil {
		return nil, fmt.Errorf("invalid host.json desired: %w", err)
	}
	return &file, nil
}

func (s Store) WriteHostDesired(desired HostDesired) error {
	normalizeHostDesired(&desired)
	if err := validateHostDesired(desired); err != nil {
		return fmt.Errorf("invalid host desired: %w", err)
	}

	file, err := s.readHostForDesiredWrite()
	if err != nil {
		return err
	}
	file.Desired = desired
	normalizeHostFile(&file)
	return writeHostFile(s.HostPath(), file)
}

func (s Store) WriteHostState(observed HostObserved, meta HostMeta) error {
	file, err := s.readHostRaw()
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("host.json is required before writing host state")
		}
		return err
	}

	var desired HostDesired
	if err := json.Unmarshal(file.Desired, &desired); err != nil {
		return fmt.Errorf("invalid host.json desired: %w", err)
	}
	normalizeHostDesired(&desired)
	if err := validateHostDesired(desired); err != nil {
		return fmt.Errorf("invalid host.json desired: %w", err)
	}

	normalizeHostObserved(&observed)
	return writeHostState(s.HostPath(), file.Desired, observed, meta)
}

func (s Store) ReadDoctor() (*DoctorFile, error) {
	var file DoctorFile
	if err := readJSON(s.DoctorPath(), &file); err != nil {
		return nil, err
	}
	if err := validateVersion("doctor.json", file.Version); err != nil {
		return nil, err
	}
	normalizeDoctorFile(&file)
	return &file, nil
}

func (s Store) WriteDoctor(file DoctorFile) error {
	if err := validateVersion("doctor.json", file.Version); err != nil {
		return err
	}
	file.Version = CurrentVersion
	normalizeDoctorFile(&file)
	return writeJSON(s.DoctorPath(), file, 0644)
}

func (s Store) ReadMembers() (*MembersFile, error) {
	var file MembersFile
	if err := readJSON(s.MembersPath(), &file); err != nil {
		if os.IsNotExist(err) {
			file = MembersFile{Version: CurrentVersion}
			normalizeMembersFile(&file)
			return &file, nil
		}
		return nil, err
	}
	if err := validateVersion("members.json", file.Version); err != nil {
		return nil, err
	}
	normalizeMembersFile(&file)
	if err := validateMembersFile(file); err != nil {
		return nil, fmt.Errorf("invalid members.json: %w", err)
	}
	return &file, nil
}

func (s Store) WriteMembers(file MembersFile) error {
	if err := validateVersion("members.json", file.Version); err != nil {
		return err
	}
	file.Version = CurrentVersion
	normalizeMembersFile(&file)
	if err := validateMembersFile(file); err != nil {
		return fmt.Errorf("invalid members.json: %w", err)
	}
	return writeJSON(s.MembersPath(), file, 0644)
}

func (s Store) ReadBoxConfig() (*BoxConfigFile, error) {
	var raw struct {
		Version int                        `json:"version"`
		Values  map[string]json.RawMessage `json:"values"`
	}
	if err := readJSON(s.BoxConfigPath(), &raw); err != nil {
		if os.IsNotExist(err) {
			return &BoxConfigFile{Version: CurrentVersion, Values: map[string]string{}}, nil
		}
		return nil, err
	}
	file := BoxConfigFile{Version: raw.Version, Values: make(map[string]string, len(raw.Values))}
	for key, rawValue := range raw.Values {
		var value string
		if err := json.Unmarshal(rawValue, &value); err != nil {
			return nil, &BoxConfigValueError{Key: key, Detail: "must be a string"}
		}
		file.Values[key] = value
	}
	if err := validateVersion("box-config.json", file.Version); err != nil {
		return nil, err
	}
	if file.Values == nil {
		file.Values = map[string]string{}
	}
	if err := ValidateBoxConfigFile(file); err != nil {
		return nil, err
	}
	return &file, nil
}

func (s Store) WriteBoxConfig(file BoxConfigFile) error {
	if err := validateVersion("box-config.json", file.Version); err != nil {
		return err
	}
	file.Version = CurrentVersion
	if file.Values == nil {
		file.Values = map[string]string{}
	}
	if err := ValidateBoxConfigFile(file); err != nil {
		return err
	}
	return writeJSON(s.BoxConfigPath(), file, 0600)
}

func (s Store) ReadApprovals() (*ApprovalsFile, error) {
	var file ApprovalsFile
	if err := readJSON(s.ApprovalsPath(), &file); err != nil {
		if os.IsNotExist(err) {
			file = ApprovalsFile{Version: CurrentVersion}
			normalizeApprovalsFile(&file)
			return &file, nil
		}
		return nil, err
	}
	if err := validateVersion("approvals.json", file.Version); err != nil {
		return nil, err
	}
	normalizeApprovalsFile(&file)
	if err := validateApprovalsFile(file); err != nil {
		return nil, fmt.Errorf("invalid approvals.json: %w", err)
	}
	return &file, nil
}

func (s Store) WriteApprovals(file ApprovalsFile) error {
	if err := validateVersion("approvals.json", file.Version); err != nil {
		return err
	}
	file.Version = CurrentVersion
	normalizeApprovalsFile(&file)
	if err := validateApprovalsFile(file); err != nil {
		return fmt.Errorf("invalid approvals.json: %w", err)
	}
	return writeJSON(s.ApprovalsPath(), file, 0644)
}

func (s Store) readHostForDesiredWrite() (HostFile, error) {
	var file HostFile
	if err := readJSON(s.HostPath(), &file); err != nil {
		if os.IsNotExist(err) {
			file = *newHostFile()
			return file, nil
		}
		return HostFile{}, err
	}
	if err := validateVersion("host.json", file.Version); err != nil {
		return HostFile{}, err
	}
	normalizeHostFile(&file)
	return file, nil
}

func (s Store) readHostRaw() (hostFileRaw, error) {
	var file hostFileRaw
	if err := readJSON(s.HostPath(), &file); err != nil {
		return hostFileRaw{}, err
	}
	if err := validateVersion("host.json", file.Version); err != nil {
		return hostFileRaw{}, err
	}
	if len(bytes.TrimSpace(file.Desired)) == 0 {
		return hostFileRaw{}, fmt.Errorf("host.json desired is required")
	}
	return file, nil
}

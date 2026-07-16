package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Store struct {
	Root    string
	VarRoot string
	RunRoot string
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

func (s Store) varRoot() string {
	if s.VarRoot != "" {
		return s.VarRoot
	}
	if root := os.Getenv("SHIP_VAR_DIR"); root != "" {
		return root
	}
	return "/var/lib/ship"
}

func (s Store) runRoot() string {
	if s.RunRoot != "" {
		return s.RunRoot
	}
	if root := os.Getenv("SHIP_RUN_DIR"); root != "" {
		return root
	}
	return "/run/ship"
}

func (s Store) VarPath(name string) string {
	return filepath.Join(s.varRoot(), name)
}

func (s Store) RunPath(name string) string {
	return filepath.Join(s.runRoot(), name)
}

func (s Store) DoctorPath() string {
	return s.VarPath("doctor.json")
}

func (s Store) MembersPath() string {
	return filepath.Join(s.root(), "members.json")
}

func (s Store) BoxConfigPath() string {
	return filepath.Join(s.root(), "box-config.json")
}

func (s Store) ApprovalsPath() string {
	return s.RunPath("approvals.json")
}

func (s Store) ApprovalsJournalPath() string {
	return s.VarPath("approvals-journal.jsonl")
}

func (s Store) UpdatesJournalPath() string {
	return s.VarPath("updates-journal.jsonl")
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
	if err := ensureDir(s.varRoot(), 0755); err != nil {
		return err
	}
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
	if err := ensureDir(s.runRoot(), 0700); err != nil {
		return err
	}
	return writeJSON(s.ApprovalsPath(), file, 0600)
}

func ensureDir(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

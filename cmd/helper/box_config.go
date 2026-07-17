package helper

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
)

type boxConfigCmd struct {
	MemberFingerprint string            `name:"member-fingerprint" hidden:"" help:"Caller SSH public key fingerprint."`
	Get               boxConfigGetCmd   `cmd:"get" help:"Read effective box configuration."`
	Set               boxConfigSetCmd   `cmd:"set" help:"Set one box configuration key."`
	Unset             boxConfigUnsetCmd `cmd:"unset" help:"Restore one box configuration key to its default."`
}

func (c boxConfigCmd) BeforeApply() error { return requireRoot() }

func (c boxConfigCmd) AfterApply() error {
	setServerMemberFingerprint(c.MemberFingerprint)
	return nil
}

type boxConfigValue struct {
	Value   string `json:"value"`
	Default string `json:"default"`
	Source  string `json:"source"`
}

type boxConfigGetResponse struct {
	Config map[string]boxConfigValue `json:"config"`
}

type boxConfigGetCmd struct{}

func (boxConfigGetCmd) Run() error {
	if _, err := authorizeHelper(helperVerbRead, authTargetForBox("get box config")); err != nil {
		utils.DieError(err, 1)
	}
	response, err := readBoxConfig()
	if err != nil {
		return err
	}
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

type boxConfigSetCmd struct {
	Key   string `arg:"" help:"Configuration key."`
	Value string `arg:"" help:"Configuration value."`
}

func (c boxConfigSetCmd) Run() error {
	if err := setBoxConfig(c.Key, c.Value, "set box config "+c.Key); err != nil {
		utils.DieError(err, 1)
	}
	fmt.Println("box config set " + c.Key)
	return nil
}

type boxConfigUnsetCmd struct {
	Key string `arg:"" help:"Configuration key."`
}

func (c boxConfigUnsetCmd) Run() error {
	if err := unsetBoxConfig(c.Key, "unset box config "+c.Key); err != nil {
		utils.DieError(err, 1)
	}
	fmt.Println("box config unset " + c.Key)
	return nil
}

func readBoxConfig() (boxConfigGetResponse, error) {
	file, err := store.Default().ReadBoxConfig()
	if err != nil {
		return boxConfigGetResponse{}, boxConfigError(err)
	}
	response := boxConfigGetResponse{Config: make(map[string]boxConfigValue, len(store.BoxConfigSchema))}
	for _, key := range store.BoxConfigSchema {
		value, set := file.Values[key.Name]
		if !set {
			value = key.Default
		}
		source := "default"
		if set {
			source = "set"
		}
		response.Config[key.Name] = boxConfigValue{Value: value, Default: key.Default, Source: source}
	}
	return response, nil
}

func boxConfigValueFor(key string) (string, error) {
	response, err := readBoxConfig()
	if err != nil {
		return "", err
	}
	value, ok := response.Config[key]
	if !ok {
		return "", boxConfigError(&store.BoxConfigKeyUnknownError{Key: key, ValidKeys: store.BoxConfigKeys()})
	}
	return value.Value, nil
}

func setBoxConfig(key, rawValue, summary string) error {
	spec, err := boxConfigKey(key)
	if err != nil {
		return err
	}
	value, err := validateBoxConfigValue(spec, rawValue)
	if err != nil {
		return err
	}
	if _, err := authorizeBoxConfigMutation(spec, authTargetForBox(summary, boxConfigTargetArg(key, value))); err != nil {
		return err
	}
	lock, err := acquireBoxConfigLock()
	if err != nil {
		return err
	}
	defer lock.Release()

	file, err := store.Default().ReadBoxConfig()
	if err != nil {
		return boxConfigError(err)
	}
	file.Values[key] = value
	if err := boxConfigError(store.Default().WriteBoxConfig(*file)); err != nil {
		return err
	}
	if err := appendUpdateJournal(updateJournalEntry{Event: "config_set", Key: key, Actor: currentServerMemberForJournal()}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write update journal: %v; next: ship box doctor\n", err)
	}
	return nil
}

func unsetBoxConfig(key, summary string) error {
	spec, err := boxConfigKey(key)
	if err != nil {
		return err
	}
	if _, err := authorizeBoxConfigMutation(spec, authTargetForBox(summary, boxConfigTargetArg(key, ""))); err != nil {
		return err
	}
	lock, err := acquireBoxConfigLock()
	if err != nil {
		return err
	}
	defer lock.Release()

	file, err := store.Default().ReadBoxConfig()
	if err != nil {
		return boxConfigError(err)
	}
	delete(file.Values, key)
	if err := boxConfigError(store.Default().WriteBoxConfig(*file)); err != nil {
		return err
	}
	if err := appendUpdateJournal(updateJournalEntry{Event: "config_unset", Key: key, Actor: currentServerMemberForJournal()}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write update journal: %v; next: ship box doctor\n", err)
	}
	return nil
}

func boxConfigKey(key string) (store.BoxConfigKey, error) {
	spec, ok := store.LookupBoxConfigKey(key)
	if !ok {
		return store.BoxConfigKey{}, boxConfigError(&store.BoxConfigKeyUnknownError{Key: key, ValidKeys: store.BoxConfigKeys()})
	}
	return spec, nil
}

func validateBoxConfigValue(spec store.BoxConfigKey, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if spec.Name == "webhook.url" {
		validated, err := validateBoxWebhookURL(value)
		if err != nil {
			return "", boxConfigError(&store.BoxConfigValueError{Key: spec.Name, Detail: err.Error()})
		}
		return validated, nil
	}
	if err := store.ValidateBoxConfigValue(spec.Name, value); err != nil {
		return "", boxConfigError(err)
	}
	return value, nil
}

func boxConfigTargetArg(key, value string) string {
	if key == "webhook.url" {
		return "key=" + key + " " + boxWebhookTargetArg(value)
	}
	if key == "box.address" {
		digest := sha256.Sum256([]byte(value))
		return "key=" + key + " address_sha256=" + fmt.Sprintf("%x", digest[:])
	}
	return "key=" + key
}

func boxConfigError(err error) error {
	if err == nil {
		return nil
	}
	var unknown *store.BoxConfigKeyUnknownError
	if errors.As(err, &unknown) {
		keys := append([]string(nil), unknown.ValidKeys...)
		sort.Strings(keys)
		return errcat.New(errcat.CodeBoxConfigKeyUnknown, errcat.Fields{
			"key":     unknown.Key,
			"valid":   strings.Join(keys, ", "),
			"command": "ship box config " + boxClientAddress(),
		})
	}
	var invalid *store.BoxConfigValueError
	if errors.As(err, &invalid) {
		return errcat.New(errcat.CodeBoxConfigValueInvalid, errcat.Fields{
			"key":     invalid.Key,
			"detail":  invalid.Detail,
			"command": "ship box config " + boxClientAddress() + " set " + invalid.Key + " <value>",
		})
	}
	return err
}

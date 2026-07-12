package helper

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
)

type notifyCmd struct {
	MemberFingerprint string         `name:"member-fingerprint" hidden:"" help:"Caller SSH public key fingerprint."`
	Get               notifyGetCmd   `cmd:"get" help:"Read the box notification webhook."`
	Set               notifySetCmd   `cmd:"set" help:"Set the box notification webhook."`
	Clear             notifyClearCmd `cmd:"clear" help:"Clear the box notification webhook."`
}

func (c notifyCmd) BeforeApply() error { return requireRoot() }

func (c notifyCmd) AfterApply() error {
	setServerMemberFingerprint(c.MemberFingerprint)
	return nil
}

type notifyGetCmd struct{}

func (notifyGetCmd) Run() error {
	if _, err := authorizeHelper(helperVerbRead, authTargetForBox("box notify")); err != nil {
		utils.DieError(err, 1)
	}
	file, err := store.Default().ReadBoxNotify()
	if err != nil {
		return err
	}
	if file.URL == "" {
		fmt.Println("box notify is unset")
		fmt.Println("next: ship box notify <box> <url>")
		return nil
	}
	fmt.Println(file.URL)
	return nil
}

type notifySetCmd struct {
	URL string `arg:"" help:"Webhook URL."`
}

func (c notifySetCmd) Run() error {
	url, err := validateBoxNotifyURL(c.URL)
	if err != nil {
		return err
	}
	if _, err := authorizeHelper(helperVerbBoxMutation, authTargetForBox("box notify set", boxNotifyTargetArg(url))); err != nil {
		utils.DieError(err, 1)
	}
	if err := store.Default().WriteBoxNotify(store.BoxNotifyFile{Version: store.CurrentVersion, URL: url}); err != nil {
		return err
	}
	fmt.Println("box notify set")
	return nil
}

func validateBoxNotifyURL(raw string) (string, error) {
	url := strings.TrimSpace(raw)
	if url == "" {
		return "", fmt.Errorf("notify must be a valid URL")
	}
	if err := config.ValidateNotifyURL(url); err != nil {
		return "", err
	}
	return url, nil
}

func boxNotifyTargetArg(url string) string {
	digest := sha256.Sum256([]byte(url))
	return "url_sha256=" + hex.EncodeToString(digest[:])
}

type notifyClearCmd struct{}

func (notifyClearCmd) Run() error {
	if _, err := authorizeHelper(helperVerbBoxMutation, authTargetForBox("box notify clear")); err != nil {
		utils.DieError(err, 1)
	}
	if err := store.Default().WriteBoxNotify(store.BoxNotifyFile{Version: store.CurrentVersion}); err != nil {
		return err
	}
	fmt.Println("box notify cleared")
	return nil
}

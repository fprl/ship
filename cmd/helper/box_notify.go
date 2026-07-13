package helper

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/fprl/ship/internal/config"
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
	url, err := boxConfigValueFor("notify.url")
	if err != nil {
		return err
	}
	if url == "" {
		fmt.Println("box notify is unset")
		fmt.Println("next: ship box notify <box> <url>")
		return nil
	}
	fmt.Println(url)
	return nil
}

type notifySetCmd struct {
	URL string `arg:"" help:"Webhook URL."`
}

func (c notifySetCmd) Run() error {
	if err := setBoxConfig("notify.url", c.URL, "box notify set"); err != nil {
		utils.DieError(err, 1)
	}
	fmt.Println("box notify set")
	return nil
}

func validateBoxNotifyURL(raw string) (string, error) {
	url := strings.TrimSpace(raw)
	if url == "" {
		return "", fmt.Errorf("notify.url cannot be empty; use ship box config <box> unset notify.url or ship box notify <box> --rm")
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
	if err := unsetBoxConfig("notify.url", "box notify clear"); err != nil {
		utils.DieError(err, 1)
	}
	fmt.Println("box notify cleared")
	return nil
}

package helper

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/utils"
)

type webhookCmd struct {
	MemberFingerprint string          `name:"member-fingerprint" hidden:"" help:"Caller SSH public key fingerprint."`
	Get               webhookGetCmd   `cmd:"get" help:"Read the box webhook."`
	Set               webhookSetCmd   `cmd:"set" help:"Set the box webhook."`
	Clear             webhookClearCmd `cmd:"clear" help:"Clear the box webhook."`
}

func (c webhookCmd) BeforeApply() error { return requireRoot() }

func (c webhookCmd) AfterApply() error {
	setServerMemberFingerprint(c.MemberFingerprint)
	return nil
}

type webhookGetCmd struct{}

func (webhookGetCmd) Run() error {
	if _, err := authorizeHelper(helperVerbRead, authTargetForBox("get box webhook")); err != nil {
		utils.DieError(err, 1)
	}
	url, err := boxConfigValueFor("webhook.url")
	if err != nil {
		return err
	}
	if url == "" {
		fmt.Fprintln(os.Stderr, "box webhook is unset")
		fmt.Fprintln(os.Stderr, "next: ship box webhook "+boxClientAddress()+" <url>")
		return nil
	}
	fmt.Println(url)
	return nil
}

type webhookSetCmd struct {
	URL string `arg:"" help:"Webhook URL."`
}

func (c webhookSetCmd) Run() error {
	if err := setBoxConfig("webhook.url", c.URL, "box webhook set"); err != nil {
		utils.DieError(err, 1)
	}
	fmt.Println("box webhook set")
	return nil
}

func validateBoxWebhookURL(raw string) (string, error) {
	url := strings.TrimSpace(raw)
	if url == "" {
		box := boxClientAddress()
		return "", fmt.Errorf("webhook.url cannot be empty; use ship box config %s unset webhook.url or ship box webhook %s --rm", box, box)
	}
	if err := config.ValidateWebhookURL(url); err != nil {
		return "", err
	}
	return url, nil
}

func boxWebhookTargetArg(url string) string {
	digest := sha256.Sum256([]byte(url))
	return "url_sha256=" + hex.EncodeToString(digest[:])
}

type webhookClearCmd struct{}

func (webhookClearCmd) Run() error {
	if err := unsetBoxConfig("webhook.url", "box webhook clear"); err != nil {
		utils.DieError(err, 1)
	}
	fmt.Println("box webhook cleared")
	return nil
}

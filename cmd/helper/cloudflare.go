package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/fprl/simple-vps/internal/cloudflare"
	"github.com/fprl/simple-vps/internal/store"
	"github.com/fprl/simple-vps/internal/utils"
)

type cloudflareCmd struct {
	SetupTunnel cloudflareSetupTunnelCmd `cmd:"setup-tunnel" help:"Create or update the Cloudflare tunnel token."`
	Publish     cloudflarePublishCmd     `cmd:"" help:"Publish a hostname through Cloudflare."`
	Remove      cloudflareRemoveCmd      `cmd:"" help:"Remove Cloudflare hostnames."`
}

type cloudflareSetupTunnelCmd struct {
	TokenFile string `name:"token-file" help:"Path to API token."`
	AccountID string `name:"account-id" help:"Cloudflare account ID."`
	Name      string `required:"" help:"Tunnel name."`
}

func (c cloudflareSetupTunnelCmd) Run() error {
	tokenFile := c.TokenFile
	if tokenFile == "" {
		tokenFile = cloudflare.CloudflareApiTokenPath()
	}

	token, err := cloudflare.ReadCloudflareApiToken(tokenFile)
	if err != nil || token == "" {
		utils.Die(fmt.Sprintf("Cloudflare API token not found: %s", tokenFile), 1)
	}

	accID, err := cloudflare.CloudflareAccountId(token, c.AccountID)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	tunnelID, err := cloudflare.EnsureCloudflareTunnel(token, accID, c.Name)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	q := url.Values{}
	res, err := cloudflare.CloudflareApiRequest(token, "GET", fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/token", accID, tunnelID), nil, q)
	if err != nil {
		utils.Die("Cloudflare API did not return a tunnel token", 1)
	}
	var tunnelToken string
	if err := json.Unmarshal(res, &tunnelToken); err != nil || tunnelToken == "" {
		tunnelToken = strings.Trim(string(res), "\"")
	}
	if tunnelToken == "" {
		utils.Die("Cloudflare API did not return a tunnel token", 1)
	}

	err = store.AtomicWrite(cloudflare.CloudflaredTunnelTokenPath(), []byte(tunnelToken+"\n"), 0640)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	_ = exec.Command("chown", "root:cloudflared", cloudflare.CloudflaredTunnelTokenPath()).Run()

	stateStore := store.Default()
	cfState, err := stateStore.ReadCloudflare()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	cfState.AccountID = accID
	cfState.TunnelID = tunnelID
	cfState.TunnelName = c.Name

	err = stateStore.WriteCloudflare(*cfState)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	fmt.Printf("Cloudflare tunnel ready: %s (%s)\n", c.Name, tunnelID)
	return nil
}

type cloudflarePublishCmd struct {
	Host string `arg:"" help:"Public hostname."`
	App  string `required:"" help:"App name."`
}

func (c cloudflarePublishCmd) Run() error {
	ingress, err := cloudflare.NewCloudflareIngress()
	if err != nil {
		if errors.Is(err, cloudflare.ErrNotConfigured) {
			fmt.Println(strings.Join([]string{
				"Cloudflare API publishing is not configured; configure this hostname in Cloudflare:",
				fmt.Sprintf("  public hostname: %s", c.Host),
				"  service: http://127.0.0.1:8080",
				"Local Caddy route publishing will continue.",
			}, "\n"))
			return nil
		}
		utils.Die(err.Error(), 1)
	}

	msg, err := ingress.Publish(c.Host, c.App)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	fmt.Println(msg)
	return nil
}

type cloudflareRemoveCmd struct {
	Host string `arg:"" optional:"" help:"Public hostname."`
	App  string `help:"App name."`
}

func (c cloudflareRemoveCmd) Run() error {
	hasHost := c.Host != ""
	hasApp := c.App != ""

	if hasHost == hasApp {
		utils.Die("provide exactly one of host or --app", 1)
	}

	ingress, err := cloudflare.NewCloudflareIngress()
	if err != nil {
		if errors.Is(err, cloudflare.ErrNotConfigured) {
			fmt.Println("Cloudflare API publishing is not configured; no Cloudflare routes to remove.")
			return nil
		}
		utils.Die(err.Error(), 1)
	}

	removed, err := ingress.Remove(c.Host, c.App)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	if len(removed) == 0 {
		fmt.Println("No Cloudflare routes matched")
		return nil
	}

	for _, h := range removed {
		fmt.Printf("Removed Cloudflare route: %s\n", h)
	}
	return nil
}

package helper

import (
	"encoding/json"
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
		utils.DieError(err, 1)
	}

	tunnelID, err := cloudflare.EnsureCloudflareTunnel(token, accID, c.Name)
	if err != nil {
		utils.DieError(err, 1)
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
		utils.DieError(err, 1)
	}

	_ = exec.Command("chown", "root:cloudflared", cloudflare.CloudflaredTunnelTokenPath()).Run()

	stateStore := store.Default()
	cfState, err := stateStore.ReadCloudflare()
	if err != nil {
		utils.DieError(err, 1)
	}
	cfState.AccountID = accID
	cfState.TunnelID = tunnelID
	cfState.TunnelName = c.Name

	err = stateStore.WriteCloudflare(*cfState)
	if err != nil {
		utils.DieError(err, 1)
	}

	fmt.Printf("Cloudflare tunnel ready: %s (%s)\n", c.Name, tunnelID)
	return nil
}

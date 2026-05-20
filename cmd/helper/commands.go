package helper

import (
	"errors"
	"os"
)

var requireRoot = func() error {
	if os.Geteuid() != 0 {
		return errors.New("this command must run as root")
	}
	return nil
}

type ServerCmd struct {
	Status        statusCmd        `cmd:"" help:"Show host status."`
	Doctor        doctorCmd        `cmd:"" help:"Run host diagnostics."`
	Route         routeCmd         `cmd:"" help:"Manage local Caddy routes."`
	Cloudflare    cloudflareCmd    `cmd:"" help:"Manage Cloudflare Tunnel ingress."`
	GenerateCaddy generateCaddyCmd `cmd:"generate-caddy" help:"Regenerate managed Caddy files."`
	App           appCmd           `cmd:"" help:"Manage app users, files, and services."`
}

func (ServerCmd) BeforeApply() error {
	return requireRoot()
}

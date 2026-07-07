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
	Doctor     doctorCmd     `cmd:"" help:"Run host diagnostics."`
	Cloudflare cloudflareCmd `cmd:"" help:"Manage Cloudflare Tunnel ingress."`
	App        appCmd        `cmd:"" help:"Manage app users, files, and processes."`
	Env        envCmd        `cmd:"" help:"Manage host environments."`
}

func (ServerCmd) BeforeApply() error {
	return requireRoot()
}

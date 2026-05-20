package helper

import (
	"github.com/alecthomas/kong"
	"github.com/fprl/simple-vps/internal/systemd"
	"github.com/fprl/simple-vps/internal/utils"
)

type serverCLI struct {
	Status        statusCmd          `cmd:"" help:"Show host status."`
	Doctor        doctorCmd          `cmd:"" help:"Run host diagnostics."`
	Route         routeCmd           `cmd:"" help:"Manage local Caddy routes."`
	Routes        routesCompatCmd    `cmd:"" hidden:"" help:"Compatibility alias for route list."`
	Cloudflare    cloudflareCmd      `cmd:"" help:"Manage Cloudflare Tunnel ingress."`
	GenerateCaddy generateCaddyCmd   `cmd:"generate-caddy" help:"Regenerate managed Caddy files."`
	App           appCmd             `cmd:"" help:"Manage app users, files, and services."`
	Publish       publishCompatCmd   `cmd:"" hidden:"" help:"Compatibility alias for route proxy."`
	Unpublish     unpublishCompatCmd `cmd:"" hidden:"" help:"Compatibility alias for route remove."`
}

func Run(command string, args []string) {
	runArgs(append([]string{command}, args...))
}

func runArgs(args []string) {
	systemd.RequireRoot()

	parser, err := kong.New(
		&serverCLI{},
		kong.Name("simple-vps"),
		kong.Description("Simple VPS privileged host API."),
		kong.UsageOnError(),
	)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	ctx, err := parser.Parse(args)
	parser.FatalIfErrorf(err)
	parser.FatalIfErrorf(ctx.Run())
}

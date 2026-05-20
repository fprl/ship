package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fprl/simple-vps/cmd/client"
	"github.com/fprl/simple-vps/cmd/helper"
)

func usage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  simple-vps init")
	fmt.Fprintln(os.Stderr, "  simple-vps check [env]")
	fmt.Fprintln(os.Stderr, "  simple-vps setup <env>")
	fmt.Fprintln(os.Stderr, "  simple-vps deploy <env> [--dirty] [--include-dotenv]")
	fmt.Fprintln(os.Stderr, "  simple-vps rollback <env> [release]")
	fmt.Fprintln(os.Stderr, "  simple-vps destroy <env> [--yes] [--confirm <app>] [--purge]")
	fmt.Fprintln(os.Stderr, "  simple-vps restart <env> <service>")
	fmt.Fprintln(os.Stderr, "  simple-vps status <env>")
	fmt.Fprintln(os.Stderr, "  simple-vps logs <env> [service] [--tail]")
	fmt.Fprintln(os.Stderr, "  simple-vps ssh <env>")
	fmt.Fprintln(os.Stderr, "  simple-vps secret put <env> <key>")
	fmt.Fprintln(os.Stderr, "  simple-vps secret list <env>")
	fmt.Fprintln(os.Stderr, "  simple-vps secret rm <env> <key>")
	fmt.Fprintln(os.Stderr, "  simple-vps env push <env> <file>")
	fmt.Fprintln(os.Stderr, "  simple-vps host status [--server <ssh-target>]")
	fmt.Fprintln(os.Stderr, "  simple-vps host doctor [--server <ssh-target>]")
	fmt.Fprintln(os.Stderr, "  simple-vps route list [--json] [--server <ssh-target>]")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	command := os.Args[1]
	args := os.Args[2:]

	if command == "--help" || command == "-h" {
		usage()
		return
	}

	switch command {
	case "init":
		client.CmdInit(".")

	case "check":
		env := ""
		if len(args) > 0 {
			env = args[0]
		}
		if len(args) > 1 {
			fmt.Fprintln(os.Stderr, "check accepts optional env")
			os.Exit(1)
		}
		client.CmdCheck(".", env)

	case "setup":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "setup requires exactly one env")
			os.Exit(1)
		}
		client.CmdSetup(".", args[0])

	case "deploy":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "deploy requires env")
			os.Exit(1)
		}
		env := args[0]
		dirty := false
		includeDotenv := false
		for _, arg := range args[1:] {
			if arg == "--dirty" {
				dirty = true
			} else if arg == "--include-dotenv" {
				includeDotenv = true
			} else {
				fmt.Fprintf(os.Stderr, "unknown argument: %s\n", arg)
				os.Exit(1)
			}
		}
		client.CmdDeploy(".", env, dirty, includeDotenv)

	case "rollback":
		if len(args) < 1 || len(args) > 2 {
			fmt.Fprintln(os.Stderr, "rollback requires env and optional release")
			os.Exit(1)
		}
		env := args[0]
		release := ""
		if len(args) == 2 {
			release = args[1]
		}
		client.CmdRollback(".", env, release)

	case "destroy":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "destroy requires env")
			os.Exit(1)
		}
		env := args[0]
		yes := false
		confirm := ""
		purge := false

		fs := flag.NewFlagSet("destroy", flag.ExitOnError)
		fs.BoolVar(&yes, "yes", false, "Confirm destruction")
		fs.StringVar(&confirm, "confirm", "", "Confirm app name")
		fs.BoolVar(&purge, "purge", false, "Purge app data")
		_ = fs.Parse(args[1:])

		client.CmdDestroy(".", env, yes, confirm, purge)

	case "restart":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "restart requires env and service")
			os.Exit(1)
		}
		client.CmdRestart(".", args[0], args[1])

	case "status":
		if len(args) == 0 {
			helper.Run("status", args)
		} else {
			if len(args) > 1 {
				fmt.Fprintln(os.Stderr, "status requires exactly one env")
				os.Exit(1)
			}
			client.CmdStatus(".", args[0])
		}

	case "logs":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "logs requires env, optional service, and optional --tail")
			os.Exit(1)
		}
		env := args[0]
		tail := false
		service := ""
		for _, arg := range args[1:] {
			if arg == "--tail" {
				tail = true
			} else if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown argument: %s\n", arg)
				os.Exit(1)
			} else {
				if service != "" {
					fmt.Fprintln(os.Stderr, "logs requires env, optional service, and optional --tail")
					os.Exit(1)
				}
				service = arg
			}
		}
		client.CmdLogs(".", env, service, tail)

	case "ssh":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "ssh requires exactly one env")
			os.Exit(1)
		}
		client.CmdSSH(".", args[0])

	case "secret":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "secret requires subcommand and env")
			os.Exit(1)
		}
		sub := args[0]
		env := args[1]
		switch sub {
		case "put":
			if len(args) != 3 {
				fmt.Fprintln(os.Stderr, "secret put requires env and key")
				os.Exit(1)
			}
			client.CmdSecretPut(".", env, args[2])
		case "list":
			if len(args) != 2 {
				fmt.Fprintln(os.Stderr, "secret list requires env")
				os.Exit(1)
			}
			client.CmdSecretList(".", env)
		case "rm":
			if len(args) != 3 {
				fmt.Fprintln(os.Stderr, "secret rm requires env and key")
				os.Exit(1)
			}
			client.CmdSecretRm(".", env, args[2])
		default:
			fmt.Fprintln(os.Stderr, "secret requires subcommand: put, list, rm")
			os.Exit(1)
		}

	case "env":
		if len(args) != 3 || args[0] != "push" {
			fmt.Fprintln(os.Stderr, "env push requires env and file")
			os.Exit(1)
		}
		client.CmdEnvPush(".", args[1], args[2])

	case "host":
		client.CmdHost(args)

	case "route":
		isClient := false
		if len(args) > 0 && args[0] == "list" {
			for _, arg := range args {
				if arg == "--server" {
					isClient = true
					break
				}
			}
			if !isClient {
				if _, err := os.Stat("simple-vps.toml"); err == nil {
					isClient = true
				}
			}
		}
		if isClient {
			client.CmdRoute(args)
		} else {
			helper.Run("route", args)
		}

	case "routes":
		helper.Run("routes", args)

	case "app", "cloudflare", "generate-caddy", "doctor", "publish", "unpublish":
		helper.Run(command, args)

	default:
		usage()
		os.Exit(1)
	}
}

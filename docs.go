package main

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fprl/simple-vps/internal/agentdocs"
	"github.com/fprl/simple-vps/internal/errcat"
)

//go:embed docs/AGENT.md
var embeddedAgentDocs string

type docsCmd struct{}

func (docsCmd) Run() error {
	return writeShipDocs(os.Stdout)
}

func writeShipDocs(w io.Writer) error {
	_, err := io.WriteString(w, embeddedAgentDocs)
	return err
}

type helpCmd struct {
	Verb []string `arg:"" optional:"" help:"Verb to explain, such as status, secret ls, or box doctor."`
	JSON bool     `name:"json" help:"Emit structured JSON."`
}

func (c helpCmd) Run() error {
	return writeShipHelp(os.Stdout, strings.Join(c.Verb, " "), c.JSON)
}

func writeShipHelp(w io.Writer, verb string, jsonFlag bool) error {
	verb = strings.Join(strings.Fields(verb), " ")
	if verb == "" {
		if jsonFlag {
			data, err := agentdocs.RenderSummaryJSON()
			if err != nil {
				return err
			}
			_, err = w.Write(data)
			return err
		}
		_, err := io.WriteString(w, agentdocs.RenderSummary())
		return err
	}
	if jsonFlag {
		data, ok, err := agentdocs.HelpJSON(verb)
		if err != nil {
			return err
		}
		if !ok {
			return unknownHelpVerb(verb)
		}
		_, err = w.Write(data)
		return err
	}
	text, ok := agentdocs.RenderHelpText(verb)
	if !ok {
		return unknownHelpVerb(verb)
	}
	_, err := io.WriteString(w, text)
	return err
}

func unknownHelpVerb(verb string) error {
	return errcat.New(errcat.CodeUsageError, errcat.Fields{
		"detail":  fmt.Sprintf("unknown help verb %q", verb),
		"command": "ship help",
	})
}

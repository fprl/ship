package helper

import (
	"encoding/json"
	"fmt"

	"github.com/fprl/ship/internal/utils"
)

type appWhyCmd struct {
	App  string `arg:"" help:"App name."`
	Env  string `arg:"" help:"Env name."`
	JSON bool   `name:"json" help:"Emit the raw deploy journal entry as JSON."`
}

func (c appWhyCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	entry, err := readLatestDeployJournalEntry(c.App, c.Env)
	if err != nil {
		utils.DieError(err, 1)
	}
	buf, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		utils.DieError(err, 1)
	}
	fmt.Println(string(buf))
	return nil
}

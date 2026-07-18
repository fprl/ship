package deployevent

import (
	"encoding/json"
	"io"
	"strings"
)

const Prefix = "ship_deploy_event:"

const (
	KindStart = "start"
	KindDone  = "done"
	KindFail  = "fail"
	KindLog   = "log"
)

type Event struct {
	Kind       string `json:"kind"`
	Phase      string `json:"phase,omitempty"`
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Message    string `json:"message,omitempty"`
}

func Write(w io.Writer, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, Prefix+string(data)+"\n")
	return err
}

func Parse(line string) (Event, bool) {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if !strings.HasPrefix(line, Prefix) {
		return Event{}, false
	}
	var event Event
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, Prefix)), &event); err != nil {
		return Event{}, false
	}
	switch event.Kind {
	case KindStart, KindDone, KindFail, KindLog:
		return event, true
	default:
		return Event{}, false
	}
}

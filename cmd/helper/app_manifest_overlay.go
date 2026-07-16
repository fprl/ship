package helper

import (
	"fmt"

	"github.com/fprl/ship/internal/config"
	"github.com/pelletier/go-toml/v2"
)

// effectiveManifestText applies the helper-only TLS overlay to the uploaded
// deploy manifest. The ordinary path returns the uploaded bytes verbatim;
// only an internal-TLS overlay needs TOML re-encoding so rollback can recover
// the exact effective route behavior from the envelope.
func effectiveManifestText(data []byte, ctx *config.AppContext) ([]byte, error) {
	internal := false
	for _, route := range ctx.Routes {
		if route.TLS == "internal" {
			internal = true
			break
		}
	}
	if !internal {
		return data, nil
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse manifest for TLS envelope: %v", err)
	}
	routes, ok := raw["routes"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("manifest routes cannot be overlaid for TLS envelope")
	}
	for name, route := range ctx.Routes {
		if route.TLS != "internal" {
			continue
		}
		key := route.Host + route.Path
		value, ok := routes[key]
		if !ok {
			value = route.Process
		}
		if process, ok := value.(string); ok {
			value = map[string]any{"process": process}
		}
		table, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("route %q cannot be overlaid for TLS envelope", name)
		}
		table["tls"] = "internal"
		routes[key] = table
	}
	encoded, err := toml.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode TLS envelope manifest: %v", err)
	}
	return encoded, nil
}

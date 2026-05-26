package generator

import "flag"

// PluginSettings holds plugin parameters passed through --ogen_opt.
type PluginSettings struct {
	// OgenConfig is the path to an ogen config file (ogen.yml). Empty means use
	// ogen defaults (and its own ogen.yml/.ogen.yml search).
	OgenConfig string
	// OpenAPIOut is a directory to write generated openapi.yaml file(s) to. Empty
	// means do not write the OpenAPI document to disk separately.
	OpenAPIOut string
}

// RegisterFlags binds plugin settings to a flag set. Wire the flag set's Set
// method as protogen.Options.ParamFunc so --ogen_opt=key=value populate these.
func (s *PluginSettings) RegisterFlags(fs *flag.FlagSet) {
	fs.StringVar(&s.OgenConfig, "ogen_config", "", "path to ogen config file (ogen.yml)")
	fs.StringVar(&s.OpenAPIOut, "openapi_out", "", "directory to write generated openapi.yaml files")
}

// NewPluginSettingsFromPlugin returns the settings already populated by the flag
// set wired in main. It exists for API symmetry with the generator.
func NewPluginSettingsFromPlugin(settings *PluginSettings) (*PluginSettings, error) {
	return settings, nil
}

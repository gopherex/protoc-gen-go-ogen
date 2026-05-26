package generator

import "google.golang.org/protobuf/compiler/protogen"

// PluginSettings holds parsed plugin parameters.
type PluginSettings struct{}

// NewPluginSettingsFromPlugin parses settings from the plugin request.
func NewPluginSettingsFromPlugin(p *protogen.Plugin) (*PluginSettings, error) {
	// TODO: parse p.Request.GetParameter()
	return &PluginSettings{}, nil
}

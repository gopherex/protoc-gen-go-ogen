package generator

import "google.golang.org/protobuf/compiler/protogen"

type Generator struct {
	Settings *PluginSettings
	Plugin   *protogen.Plugin
}

func NewGenerator(p *protogen.Plugin, settings *PluginSettings) (*Generator, error) {
	return &Generator{
		Settings: settings,
		Plugin:   p,
	}, nil
}

func (g *Generator) Generate() error {
	return NewOpenAPIGenerator(g.Plugin, g.Settings).Generate()
}

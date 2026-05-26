package main

import (
	"github.com/yaroher/protoc-gen-ogen/generator"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/types/pluginpb"
)

func Generate(p *protogen.Plugin) error {
	settings, err := generator.NewPluginSettingsFromPlugin(p)
	if err != nil {
		return err
	}
	g, err := generator.NewGenerator(p, settings)
	if err != nil {
		return err
	}
	return g.Generate()
}

func main() {
	protogen.Options{}.Run(func(plugin *protogen.Plugin) error {
		plugin.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
		return Generate(plugin)
	})
}

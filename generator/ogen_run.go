package generator

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-faster/yaml"
	"github.com/gopherex/protoc-gen-go-ogen/ogen"
	ogenlib "github.com/ogen-go/ogen"
	ogengen "github.com/ogen-go/ogen/gen"
	"go.uber.org/zap"
	"google.golang.org/protobuf/compiler/protogen"
)

// memFS captures ogen-generated files in memory so they can be emitted through
// the protoc CodeGeneratorResponse instead of written to disk directly.
type memFS struct {
	files map[string][]byte
}

func (m *memFS) WriteFile(baseName string, source []byte) error {
	m.files[baseName] = source
	return nil
}

// generateOgen runs ogen in-process on the generated OpenAPI document and emits
// the resulting Go files under the file's ogen target directory, relative to
// protoc's --ogen_out.
func (g *OpenAPIGenerator) generateOgen(file *protogen.File, fileOpts *ogen.FileOptions, specBytes []byte) error {
	opts, err := loadOgenConfig(g.Settings.OgenConfig)
	if err != nil {
		return err
	}
	spec, err := ogenlib.Parse(specBytes)
	if err != nil {
		return fmt.Errorf("parse generated openapi for ogen: %w", err)
	}
	generator, err := ogengen.NewGenerator(spec, opts)
	if err != nil {
		return fmt.Errorf("ogen build IR: %w", err)
	}
	fsys := &memFS{files: map[string][]byte{}}
	if err := generator.WriteSource(fsys, ogenPackageName(fileOpts)); err != nil {
		return fmt.Errorf("ogen write source: %w", err)
	}
	target := ogenTargetDir(fileOpts)
	names := make([]string, 0, len(fsys.files))
	for name := range fsys.files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out := name
		if target != "" {
			out = path.Join(target, name)
		}
		gf := g.Plugin.NewGeneratedFile(out, "")
		if _, err := gf.Write(fsys.files[name]); err != nil {
			return err
		}
	}

	if fileOpts.GetGenerateConverters() {
		fakerEnabled := false
		if feats, ferr := opts.Generator.Features.Build(); ferr == nil {
			fakerEnabled = feats.Has(ogengen.DebugExampleTests)
		}
		if err := g.generateConverters(file, fileOpts, generator, fakerEnabled); err != nil {
			return err
		}
		if fileOpts.GetGenerateGrpcAdapter() {
			g.generateAdapter(file, fileOpts, generator)
		}
	}
	return nil
}

// loadOgenConfig mirrors ogen's cmd loader: decode the YAML config into
// gen.Options with strict unknown-field checking. An empty path yields defaults.
func loadOgenConfig(cfgPath string) (ogengen.Options, error) {
	opts := ogengen.Options{Logger: zap.NewNop()}
	if cfgPath == "" {
		return opts, nil
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return opts, fmt.Errorf("read ogen config %q: %w", cfgPath, err)
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(&opts); err != nil {
		return opts, fmt.Errorf("parse ogen config %q: %w", cfgPath, err)
	}
	opts.Logger = zap.NewNop()
	return opts, nil
}

// ogenPackageName resolves the short Go package name for ogen output.
func ogenPackageName(opts *ogen.FileOptions) string {
	if name := opts.GetOgenPackageName(); name != "" {
		return name
	}
	if pkg := lastSegment(opts.GetOgenPackage()); pkg != "" {
		return pkg
	}
	if target := lastSegment(opts.GetOgenTarget()); target != "" {
		return target
	}
	return "api"
}

// ogenTargetDir is the slash-relative subdirectory (under --ogen_out) for ogen
// output files.
func ogenTargetDir(opts *ogen.FileOptions) string {
	return strings.Trim(opts.GetOgenTarget(), "/")
}

// lastSegment returns the trailing identifier of a Go package path, honoring the
// "import/path;name" form used by go_package.
func lastSegment(p string) string {
	p = strings.Trim(p, "/")
	if i := strings.LastIndex(p, ";"); i >= 0 {
		return p[i+1:]
	}
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// writeOpenAPIFile writes the OpenAPI document to dir/name on disk, creating dir.
func writeOpenAPIFile(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create openapi out dir %q: %w", dir, err)
	}
	target := filepath.Join(dir, name)
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return fmt.Errorf("write openapi file %q: %w", target, err)
	}
	return nil
}

// openapiFileName is the base file name for a file's OpenAPI document.
func openapiFileName(file *protogen.File, opts *ogen.FileOptions) string {
	if out := opts.GetOpenapiOutput(); out != "" {
		return path.Base(out)
	}
	return file.GeneratedFilenamePrefix + ".openapi.yaml"
}

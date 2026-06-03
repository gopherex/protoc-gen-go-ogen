package generator

import (
	"strings"
	"testing"

	"github.com/gopherex/protoc-gen-go-ogen/ogen"
)

func TestMergeOperationMaps(t *testing.T) {
	t.Run("disjoint paths merge", func(t *testing.T) {
		dst := map[string]any{}
		if err := mergeOperationMaps(dst, map[string]any{
			"/a": map[string]any{"get": map[string]any{"operationId": "a"}},
		}, "path"); err != nil {
			t.Fatalf("first merge: %v", err)
		}
		if err := mergeOperationMaps(dst, map[string]any{
			"/b": map[string]any{"post": map[string]any{"operationId": "b"}},
		}, "path"); err != nil {
			t.Fatalf("second merge: %v", err)
		}
		if len(dst) != 2 {
			t.Fatalf("want 2 paths, got %d: %v", len(dst), dst)
		}
	})

	t.Run("same path different methods merge", func(t *testing.T) {
		dst := map[string]any{"/a": map[string]any{"get": map[string]any{"operationId": "g"}}}
		if err := mergeOperationMaps(dst, map[string]any{
			"/a": map[string]any{"post": map[string]any{"operationId": "p"}},
		}, "path"); err != nil {
			t.Fatalf("merge: %v", err)
		}
		item := dst["/a"].(map[string]any)
		if len(item) != 2 {
			t.Fatalf("want get+post on /a, got %v", item)
		}
	})

	t.Run("same path same method collides", func(t *testing.T) {
		dst := map[string]any{"/a": map[string]any{"get": map[string]any{"operationId": "g1"}}}
		err := mergeOperationMaps(dst, map[string]any{
			"/a": map[string]any{"get": map[string]any{"operationId": "g2"}},
		}, "path")
		if err == nil {
			t.Fatal("want collision error, got nil")
		}
		if !strings.Contains(err.Error(), "GET") || !strings.Contains(err.Error(), "/a") {
			t.Fatalf("error should name the collision: %v", err)
		}
	})
}

func TestCheckUniqueOperationIDs(t *testing.T) {
	mkOp := func(id string) map[string]any { return map[string]any{"operationId": id} }

	t.Run("unique across paths and webhooks ok", func(t *testing.T) {
		paths := map[string]any{
			"/a": map[string]any{"get": mkOp("a")},
			"/b": map[string]any{"post": mkOp("b")},
		}
		webhooks := map[string]any{
			"hook": map[string]any{"post": mkOp("c")},
		}
		if err := checkUniqueOperationIDs(paths, webhooks); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})

	t.Run("duplicate within paths", func(t *testing.T) {
		paths := map[string]any{
			"/a": map[string]any{"get": mkOp("dup")},
			"/b": map[string]any{"get": mkOp("dup")},
		}
		err := checkUniqueOperationIDs(paths)
		if err == nil || !strings.Contains(err.Error(), "dup") {
			t.Fatalf("want duplicate error mentioning id, got %v", err)
		}
	})

	t.Run("duplicate across paths and webhooks", func(t *testing.T) {
		paths := map[string]any{"/a": map[string]any{"get": mkOp("x")}}
		webhooks := map[string]any{"hook": map[string]any{"post": mkOp("x")}}
		if err := checkUniqueOperationIDs(paths, webhooks); err == nil {
			t.Fatal("want cross-map duplicate error, got nil")
		}
	})

	t.Run("missing operationId ignored", func(t *testing.T) {
		paths := map[string]any{
			"/a": map[string]any{"get": map[string]any{}},
			"/b": map[string]any{"get": map[string]any{}},
		}
		if err := checkUniqueOperationIDs(paths); err != nil {
			t.Fatalf("empty ids must not collide, got %v", err)
		}
	})
}

func TestApplyObjectOneofGroups(t *testing.T) {
	t.Run("single oneof stores oneOf marker", func(t *testing.T) {
		schema := map[string]any{}
		applyObjectOneofGroups(schema, [][]any{{
			map[string]any{"required": []any{"a"}},
			map[string]any{"required": []any{"b"}},
		}})
		m, ok := schema[objectOneofMarker].(map[string]any)
		if !ok {
			t.Fatalf("marker missing: %v", schema)
		}
		if _, ok := m["oneOf"]; !ok {
			t.Fatalf("want oneOf, got %v", m)
		}
	})

	t.Run("multiple oneofs combine under allOf", func(t *testing.T) {
		schema := map[string]any{}
		applyObjectOneofGroups(schema, [][]any{
			{map[string]any{"required": []any{"a"}}},
			{map[string]any{"required": []any{"b"}}},
		})
		m := schema[objectOneofMarker].(map[string]any)
		if _, ok := m["allOf"]; !ok {
			t.Fatalf("want allOf, got %v", m)
		}
	})

	t.Run("empty groups add nothing", func(t *testing.T) {
		schema := map[string]any{}
		applyObjectOneofGroups(schema, nil)
		if _, ok := schema[objectOneofMarker]; ok {
			t.Fatal("no marker expected for empty groups")
		}
	})
}

func TestFinalizeObjectOneof(t *testing.T) {
	build := func() map[string]any {
		return map[string]any{
			"components": map[string]any{"schemas": map[string]any{
				"X": map[string]any{
					"type":       "object",
					"properties": map[string]any{"a": map[string]any{}, "b": map[string]any{}},
					objectOneofMarker: map[string]any{"oneOf": []any{
						map[string]any{"required": []any{"a"}},
						map[string]any{"required": []any{"b"}},
					}},
				},
			}},
		}
	}

	t.Run("detects marker", func(t *testing.T) {
		if !containsObjectOneof(build()) {
			t.Fatal("want marker detected")
		}
		if containsObjectOneof(map[string]any{"a": 1}) {
			t.Fatal("false positive")
		}
	})

	t.Run("expand merges oneOf and drops marker", func(t *testing.T) {
		doc := build()
		finalizeObjectOneof(doc, true)
		x := doc["components"].(map[string]any)["schemas"].(map[string]any)["X"].(map[string]any)
		if _, ok := x[objectOneofMarker]; ok {
			t.Fatal("marker must be removed")
		}
		if _, ok := x["oneOf"]; !ok {
			t.Fatalf("oneOf must be expanded: %v", x)
		}
	})

	t.Run("strip drops marker without oneOf", func(t *testing.T) {
		doc := build()
		finalizeObjectOneof(doc, false)
		x := doc["components"].(map[string]any)["schemas"].(map[string]any)["X"].(map[string]any)
		if _, ok := x[objectOneofMarker]; ok {
			t.Fatal("marker must be removed")
		}
		if _, ok := x["oneOf"]; ok {
			t.Fatal("ogen doc must not carry oneOf")
		}
	})

	t.Run("deepCopyNode isolates mutation", func(t *testing.T) {
		doc := build()
		clone := deepCopyNode(doc).(map[string]any)
		finalizeObjectOneof(clone, false)
		// Original still carries the marker.
		if !containsObjectOneof(doc) {
			t.Fatal("deepCopyNode must not mutate the original")
		}
	})
}

func TestHasDocLevelOptions(t *testing.T) {
	if hasDocLevelOptions(nil) {
		t.Fatal("nil must be false")
	}
	// generate_openapi alone is the inclusion marker, not a doc-level option.
	if hasDocLevelOptions(&ogen.FileOptions{GenerateOpenapi: true}) {
		t.Fatal("generate_openapi-only must be false")
	}
	cases := []*ogen.FileOptions{
		{GenerateOgen: true},
		{GenerateConverters: true},
		{GenerateGrpcAdapter: true},
		{Title: "x"},
		{OpenapiVersion: "3.1.0"},
		{OgenPackage: "example.com/x"},
		{Servers: []*ogen.Server{{Url: "https://x"}}},
		{Tags: []*ogen.Tag{{Name: "t"}}},
		{Extensions: []*ogen.NamedString{{Name: "x-a", Value: "1"}}},
	}
	for i, o := range cases {
		if !hasDocLevelOptions(o) {
			t.Fatalf("case %d must be doc-level: %v", i, o)
		}
	}
}

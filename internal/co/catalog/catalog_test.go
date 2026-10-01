package catalog_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/co/store"
	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/ocitest"
)

const (
	repoA   = "registry.test/example/hello"
	repoB   = "registry.test/mirror/hello"
	appID   = "com-example-hello"
	version = "1.2.3"
)

// description is a valid Margo Application Description with one compose profile. Each test case
// breaks it with one replacement.
const description = `apiVersion: margo.org/v1-alpha1
id: com-example-hello
metadata:
  name: Hello
  version: 1.2.3
  catalog:
    organization:
      - name: Example
deploymentProfiles:
  - type: compose
    id: hello-compose
    components:
      - name: web
        properties:
          repository: oci://registry.test/example/hello-web
          revision: 1.2.3_build.5
parameters:
  greeting:
    value: Hello
    targets:
      - pointer: GREETING
        components: [web]
`

type fixture struct {
	reg     *ocitest.Registry
	store   *store.Memory
	catalog *catalog.Catalog
}

func newFixture() fixture {
	reg := ocitest.NewRegistry()
	s := store.NewMemory()
	return fixture{reg: reg, store: s, catalog: catalog.New(reg.Open, s)}
}

// SPEC §17.2: "Importing an invalid Application Description is rejected with the reason"
//
// Each case breaks one rule of SPEC §5.3; the import fails with ErrInvalidPackage, the error
// names the reason, and nothing is stored.
func TestSpec_17_2_ImportRejectsInvalidApplicationDescription(t *testing.T) {
	descLayer := func(data string) ocitest.Layer {
		return ocitest.Layer{MediaType: contract.AppDescriptionMediaType, Title: "margo.yaml", Data: []byte(data)}
	}
	tests := []struct {
		name string
		push func(t *testing.T, reg *ocitest.Registry)
		// reason is a part of the error message that names the broken rule.
		reason string
	}{
		{
			name: "manifest is not a Margo application package",
			push: func(t *testing.T, reg *ocitest.Registry) {
				reg.Push(t, repoA, version, ocitest.Artifact{
					ArtifactType: "application/vnd.example.other+json", Layers: []ocitest.Layer{descLayer(description)}})
			},
			reason: "artifactType",
		},
		{
			name: "package has no Application Description layer",
			push: func(t *testing.T, reg *ocitest.Registry) {
				reg.Push(t, repoA, version, ocitest.Artifact{ArtifactType: contract.AppPackageArtifactType,
					Layers: []ocitest.Layer{{MediaType: "application/vnd.margo.app.icon.v1+png", Data: []byte("png")}}})
			},
			reason: "description layer",
		},
		{
			name: "package has two Application Description layers",
			push: func(t *testing.T, reg *ocitest.Registry) {
				reg.Push(t, repoA, version, ocitest.Artifact{ArtifactType: contract.AppPackageArtifactType,
					Layers: []ocitest.Layer{descLayer(description), descLayer(description + "# second\n")}})
			},
			reason: "description layer",
		},
		{
			name: "package config is not empty",
			push: func(t *testing.T, reg *ocitest.Registry) {
				reg.Push(t, repoA, version, ocitest.Artifact{
					ArtifactType: contract.AppPackageArtifactType,
					Config:       &ocitest.Layer{MediaType: "application/vnd.oci.image.config.v1+json", Data: []byte(`{"os":"linux"}`)},
					Layers:       []ocitest.Layer{descLayer(description)},
				})
			},
			reason: "config",
		},
		{
			name:   "fails schema validation: id with uppercase letters",
			push:   pushApp(replace("id: com-example-hello", "id: Com_Example_Hello")),
			reason: "/id",
		},
		{
			name:   "fails schema validation: not YAML",
			push:   pushApp(func(string) string { return "id: [unclosed\n" }),
			reason: "application description",
		},
		{
			name: "metadata.version differs from the tag",
			push: func(t *testing.T, reg *ocitest.Registry) {
				reg.PushApp(t, repoA, version, []byte(strings.Replace(description, "version: 1.2.3", "version: 1.2.4", 1)))
			},
			reason: "metadata.version",
		},
		{
			name:   "no compose deployment profile",
			push:   pushApp(replace("type: compose", "type: helm")),
			reason: "compose",
		},
		{
			name:   "component repository is not an oci:// reference",
			push:   pushApp(replace("oci://registry.test", "https://registry.test")),
			reason: "/repository",
		},
		{
			name:   "component revision is not SemVer in the §4.2 form",
			push:   pushApp(replace("revision: 1.2.3_build.5", "revision: v1.2.3")),
			reason: "/revision",
		},
		{
			name:   "component revision uses + for build metadata",
			push:   pushApp(replace("revision: 1.2.3_build.5", "revision: 1.2.3+build.5")),
			reason: "/revision",
		},
		{
			name:   "parameter target names a component in no profile",
			push:   pushApp(replace("components: [web]", "components: [web, api]")),
			reason: `"api"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture()
			tt.push(t, f.reg)

			_, err := f.catalog.Import(context.Background(), repoA, version)
			if !errors.Is(err, catalog.ErrInvalidPackage) {
				t.Fatalf("Import: got error %v, want ErrInvalidPackage", err)
			}
			if !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("Import error %q does not name the reason %q", err, tt.reason)
			}
			if _, ok, _ := f.store.App(context.Background(), appID, version); ok {
				t.Error("a rejected import stored the app")
			}
		})
	}

	t.Run("duplicates an imported version with different content", func(t *testing.T) {
		ctx := context.Background()
		f := newFixture()
		f.reg.PushApp(t, repoA, version, []byte(description))
		first, err := f.catalog.Import(ctx, repoA, version)
		if err != nil {
			t.Fatalf("first Import: %v", err)
		}
		f.reg.PushApp(t, repoB, version, []byte(strings.Replace(description, "name: Hello", "name: Hello again", 1)))

		_, err = f.catalog.Import(ctx, repoB, version)
		if !errors.Is(err, catalog.ErrConflict) {
			t.Fatalf("second Import: got error %v, want ErrConflict", err)
		}
		if !strings.Contains(err.Error(), appID) || !strings.Contains(err.Error(), version) {
			t.Errorf("Import error %q does not name %s %s", err, appID, version)
		}
		stored, _, _ := f.store.App(ctx, appID, version)
		if !bytes.Equal(stored.Description, first.Description) || stored.Repository != repoA {
			t.Error("the conflicting import changed the stored app")
		}
	})
}

// SPEC §17.2: "Re-importing an identical version succeeds without changes"
func TestSpec_17_2_ReimportIdenticalVersionChangesNothing(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	f.reg.PushApp(t, repoA, version, []byte(description))
	first, err := f.catalog.Import(ctx, repoA, version)
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	// The same bytes from another repository (a mirror) are the same version.
	f.reg.PushApp(t, repoB, version, []byte(description))

	again, err := f.catalog.Import(ctx, repoB, version)
	if err != nil {
		t.Fatalf("re-Import: %v", err)
	}
	stored, ok, _ := f.store.App(ctx, appID, version)
	if !ok {
		t.Fatal("app not stored")
	}
	for name, got := range map[string]catalog.App{"re-Import result": again, "stored app": stored} {
		if got.Repository != repoA || got.Digest != first.Digest || !bytes.Equal(got.Description, first.Description) {
			t.Errorf("%s = {%s %s}, want the first import {%s %s}", name, got.Repository, got.Digest, repoA, first.Digest)
		}
	}
}

func TestImportStoresExactDescriptionBytes(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	f.reg.PushApp(t, repoA, version, []byte(description))

	app, err := f.catalog.Import(ctx, repoA, version)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	want := catalog.App{
		ID: appID, Version: version, Repository: repoA,
		Description: []byte(description), Digest: contract.DigestOf([]byte(description)),
	}
	stored, ok, _ := f.store.App(ctx, appID, version)
	if !ok {
		t.Fatal("app not stored")
	}
	for name, got := range map[string]catalog.App{"Import result": app, "stored app": stored} {
		if got.ID != want.ID || got.Version != want.Version || got.Repository != want.Repository ||
			got.Digest != want.Digest || !bytes.Equal(got.Description, want.Description) {
			t.Errorf("%s = %+v, want %+v", name, got, want)
		}
	}
}

func TestImportUnknownTagIsNotInvalidPackage(t *testing.T) {
	f := newFixture()
	f.reg.PushApp(t, repoA, version, []byte(description))

	_, err := f.catalog.Import(context.Background(), repoA, "9.9.9")
	if err == nil || errors.Is(err, catalog.ErrInvalidPackage) {
		t.Fatalf("Import of a missing tag: got %v, want a registry error", err)
	}
}

// pushApp returns a push step that stores the edited description as a Margo package.
func pushApp(edit func(string) string) func(*testing.T, *ocitest.Registry) {
	return func(t *testing.T, reg *ocitest.Registry) {
		reg.PushApp(t, repoA, version, []byte(edit(description)))
	}
}

func replace(old, new string) func(string) string {
	return func(s string) string {
		if !strings.Contains(s, old) {
			panic("fixture does not contain " + old)
		}
		return strings.Replace(s, old, new, 1)
	}
}

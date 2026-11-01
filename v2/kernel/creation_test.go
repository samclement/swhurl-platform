package kernel

import (
	"strings"
	"testing"
)

// A create states a staging instance from what an image says about itself, and every refusal it earns is the
// record's own rule.

// created is a creation of the fixture release that an operator could make: a web image with a database, the 128 MiB
// it says it runs in, the one-gigabyte volume it states, private exposure and a fresh broker identity.
func created(t *testing.T, declaration Declaration) Creation {
	t.Helper()
	release := release(t)
	return Creation{Application: "demo-orders", Team: "demo", Release: release,
		Declaration: declaration, Exposure: "private", Key: NewUserKey([32]byte{7}).Public}
}

func webDeclaration(release ReleaseBundle) Declaration {
	return Declaration{SchemaVersion: 2, Runtime: release.Runtime, Telemetry: true, Database: "sqlite",
		DatabaseSizeMiB: 1024, MemoryMiB: 128}
}

func TestCreationIsTheStagingInstanceAnImageStates(t *testing.T) {
	release := release(t)
	spec, app, err := created(t, webDeclaration(release)).Instance()
	if err != nil {
		t.Fatal(err)
	}
	if spec.Environment != "staging" || spec.Configuration.Replicas != 1 || spec.Configuration.MemoryMiB != 128 {
		t.Errorf("the instance: %+v", spec)
	}
	if spec.Storage.SizeMiB != 1024 || spec.Storage.ClaimName != "" {
		t.Errorf("the volume: %+v", spec.Storage)
	}
	if spec.Grants.Exposure != "private" || spec.Release != release.ReleaseID ||
		spec.Events.Key != NewUserKey([32]byte{7}).Public {
		t.Errorf("the instance: %+v", spec)
	}
	if app.Team != "demo" || app.Application != "demo-orders" {
		t.Errorf("the app record: %+v", app)
	}
	// A created pair is a record like any other: both identities are computable from what it states.
	if _, err := spec.Revision(); err != nil {
		t.Errorf("the instance revision: %v", err)
	}
	if _, err := app.ID(); err != nil {
		t.Errorf("the app identity: %v", err)
	}
}

// The memory and the volume are the image's facts: the instance runs in the memory the image says it runs in, and a
// database is created with the size the image states.
func TestCreationTakesTheImagesSizes(t *testing.T) {
	release := release(t)
	declaration := webDeclaration(release)
	declaration.MemoryMiB, declaration.DatabaseSizeMiB = 192, 2048
	spec, _, err := created(t, declaration).Instance()
	if err != nil {
		t.Fatal(err)
	}
	if spec.Configuration.MemoryMiB != 192 || spec.Storage.SizeMiB != 2048 {
		t.Errorf("sizes: memory %d, volume %d", spec.Configuration.MemoryMiB, spec.Storage.SizeMiB)
	}
}

func TestCreationRefusals(t *testing.T) {
	release := release(t)
	declaration := webDeclaration(release)
	other := release
	other.Application = "demo-shop"
	for _, tt := range []struct {
		name      string
		change    func(*Creation)
		declared  Declaration
		want      Code
		expectErr string
	}{
		{"no memory", func(c *Creation) {}, Declaration{SchemaVersion: 2,
			Runtime: release.Runtime, Database: "sqlite", DatabaseSizeMiB: 1024}, Invalid, "declaration.memoryMiB"},
		{"a database with no volume", func(c *Creation) {}, Declaration{SchemaVersion: 2,
			Runtime: release.Runtime, Database: "sqlite", MemoryMiB: 128}, Invalid, "volume size"},
		{"a volume with nothing to keep", func(c *Creation) {}, Declaration{SchemaVersion: 2,
			Runtime: release.Runtime, DatabaseSizeMiB: 1024, MemoryMiB: 128}, Invalid, "volume would hold nothing"},
		{"an exposure v2 does not model", func(c *Creation) { c.Exposure = "public" }, declaration,
			Unsupported, "grants.exposure"},
		{"no team", func(c *Creation) { c.Team = "" }, declaration, Invalid, "team"},
		{"another app's release", func(c *Creation) { c.Release = other }, declaration,
			WrongApplication, "application"},
		{"an image that needs secrets", func(c *Creation) {}, Declaration{SchemaVersion: 2,
			Runtime: release.Runtime, Database: "sqlite", DatabaseSizeMiB: 1024, MemoryMiB: 128,
			Secrets: []string{"API_KEY"}}, Unsupported, "bindings_are_not_modelled"},
		{"a key that is not a public nkey", func(c *Creation) { c.Key = "not-a-key" }, declaration,
			Invalid, "events.key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			creation := created(t, tt.declared)
			tt.change(&creation)
			_, _, err := creation.Instance()
			code(t, err, tt.want)
			if tt.expectErr != "" && !strings.Contains(err.Error(), tt.expectErr) {
				t.Errorf("error = %v, want it to name %q", err, tt.expectErr)
			}
		})
	}
}

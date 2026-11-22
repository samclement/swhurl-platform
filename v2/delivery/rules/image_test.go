package rules

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

const imageDeclaration = `{"schemaVersion":2,"runtime":{"kind":"web","port":8080,"healthPath":"/healthz","uid":65532},"memoryMiB":128}`

func TestImageMetadataOf(t *testing.T) {
	for _, suffix := range []string{commit[:7], commit} {
		t.Run(suffix, func(t *testing.T) {
			tag, found := kernel.NewestTag([]string{"43-" + suffix})
			if !found {
				t.Fatal("test tag must satisfy the platform contract")
			}
			declaration, revision, err := ImageMetadataOf(tag, imageDeclaration, commit)
			want := kernel.Declaration{SchemaVersion: 2, Runtime: kernel.Runtime{
				Kind: "web", Port: 8080, HealthPath: "/healthz", UID: 65532}, MemoryMiB: 128}
			if err != nil || revision != commit || !reflect.DeepEqual(declaration, want) {
				t.Fatalf("ImageMetadataOf = %+v, %q, %v; want %+v, %q", declaration, revision, err, want, commit)
			}
		})
	}
}

func TestImageMetadataOfRefusals(t *testing.T) {
	tag, _ := kernel.NewestTag([]string{"43-" + commit[:7]})
	for _, tt := range []struct {
		name, declaration, revision, want string
		missing                           bool
	}{
		{"absent declaration before revision", "", "", "invalid: declaration: absent", true},
		{"malformed declaration before revision", "{", "", "invalid: document: expected one valid UTF-8 JSON document", false},
		{"unsupported declaration before revision", strings.Replace(imageDeclaration, `"schemaVersion":2`, `"schemaVersion":3`, 1), "", "unsupported: schemaVersion: only version 2 is supported", false},
		{"absent revision", imageDeclaration, "", "invalid: source: revision_absent", true},
		{"abbreviated revision", imageDeclaration, commit[:7], "invalid: source.revision: invalid format", false},
		{"malformed revision", imageDeclaration, strings.Repeat("z", 40), "invalid: source.revision: invalid format", false},
		{"mismatched revision", imageDeclaration, other, "invalid: source.revision: the tag's suffix does not abbreviate the revision", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			declaration, revision, err := ImageMetadataOf(tag, tt.declaration, tt.revision)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %T %v, want %q", err, err, tt.want)
			}
			if tt.missing {
				var missing *Error
				if !errors.As(err, &missing) || missing.Code != Invalid {
					t.Fatalf("absent metadata must be a delivery rule error, got %T %v", err, err)
				}
			} else {
				var invalid *kernel.Error
				if !errors.As(err, &invalid) {
					t.Fatalf("malformed metadata must retain the kernel error, got %T", err)
				}
			}
			if revision != "" || !reflect.DeepEqual(declaration, kernel.Declaration{}) {
				t.Fatalf("a rejection returned accepted metadata: %+v, %q", declaration, revision)
			}
		})
	}
}

package secretcheck

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

// secret is one Secret document with stringData values.
func secret(namespace, name string, values map[string]any) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "Secret",
		"metadata":   map[string]any{"namespace": namespace, "name": name},
		"stringData": values}
}

// platform is every file the comparisons read, each holding its copies with equal values, so a test changes only
// what it is about.
func platform() []File {
	file := func(path string, values map[string]any) File {
		return File{Path: path, Documents: []map[string]any{secret("ns", "s", values)}}
	}
	return []File{
		file("platform/alerts/secret-platform.sops.yaml", map[string]any{"address": "https://ntfy.sh/topic?tpl=1"}),
		file("platform/clickstack/secret.sops.yaml", map[string]any{IngestionKey: "ingest"}),
		file("platform/console/notification-secret.sops.yaml", map[string]any{"NTFY_PLATFORM_URL": "https://ntfy.sh/topic"}),
		file("platform/console/secret.sops.yaml", map[string]any{"IMAGE_WEBHOOK_TOKEN": "hook", "GITHUB_TOKEN": "ghp"}),
		file("platform/flux-webhook/image-secret.sops.yaml", map[string]any{"token": "hook"}),
		file("platform/image-automation/dispatch-secret.sops.yaml", map[string]any{"token": "ghp"}),
		file("platform/otel/secret.sops.yaml", map[string]any{IngestionKey: "ingest"}),
	}
}

// set replaces one value in the platform's files.
func set(files []File, path, key string, value any) []File {
	for _, file := range files {
		if file.Path == path {
			file.Documents[0]["stringData"].(map[string]any)[key] = value
		}
	}
	return files
}

func texts(report Report, level Level) []string {
	var out []string
	for _, line := range report.Lines {
		if line.Level == level {
			out = append(out, line.Text)
		}
	}
	return out
}

func TestThePlatformPasses(t *testing.T) {
	report := Check(platform())
	if report.Errors != 0 || report.Warnings != 0 {
		t.Fatalf("errors %d, warnings %d: %v", report.Errors, report.Warnings, texts(report, Error))
	}
	want := []string{
		"CLICKSTACK_INGESTION_KEY is identical in platform/clickstack/secret.sops.yaml and platform/otel/secret.sops.yaml",
		"ntfy destinations match the notification checker copies (compared by hashes)",
		"the image webhook token is identical in platform/flux-webhook/image-secret.sops.yaml and platform/console/secret.sops.yaml",
		"the GitHub dispatch token is identical in platform/image-automation/dispatch-secret.sops.yaml and platform/console/secret.sops.yaml",
	}
	if got := texts(report, OK); !slices.Equal(got[len(got)-4:], want) {
		t.Fatalf("comparisons = %q", got[len(got)-4:])
	}
	if got := texts(report, OK)[0]; got != "platform/alerts/secret-platform.sops.yaml: ns/s (1 stringData)" {
		t.Fatalf("summary = %q", got)
	}
}

func TestNotificationDestinationsMatchWithoutReportingValues(t *testing.T) {
	if got := destinationProblem(map[string]string{"source-platform": "a", "checker-platform": "a"}); got != "" {
		t.Fatalf("equal copies = %q", got)
	}
	got := destinationProblem(map[string]string{"source-platform": "a", "checker-platform": "private-fingerprint"})
	if !strings.Contains(got, "differs") || strings.Contains(got, "private-fingerprint") {
		t.Fatalf("differing copies = %q", got)
	}
	if got := destinationProblem(map[string]string{}); !strings.Contains(got, "missing") {
		t.Fatalf("no copies = %q", got)
	}
	// The provider's copy keeps its rendering query; the checker's does not.
	report := Check(set(platform(), "platform/alerts/secret-platform.sops.yaml", "address", "https://ntfy.sh/other?tpl=1"))
	if errors := texts(report, Error); len(errors) != 1 || !strings.Contains(errors[0], "ntfy platform destination differs") {
		t.Fatalf("another topic = %q", errors)
	}
}

func TestDoubleEncoding(t *testing.T) {
	uuid := []byte("0f8fad5b-d9cb-469f-a165-70867728950e")
	if LooksDoubleEncoded(uuid) {
		t.Fatal("a plain token is not double-encoded")
	}
	if !LooksDoubleEncoded([]byte(base64.StdEncoding.EncodeToString(uuid))) {
		t.Fatal("base64 of a token is")
	}
	binary := make([]byte, 40)
	for i := range binary {
		binary[i] = byte(i)
	}
	if LooksDoubleEncoded(binary) {
		t.Fatal("binary bytes are not base64 text")
	}
	twice := base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString(uuid)))
	files := append(platform(), File{Path: "platform/x.sops.yaml", Documents: []map[string]any{{
		"metadata": map[string]any{"namespace": "ns", "name": "x"}, "data": map[string]any{"token": twice}}}})
	report := Check(files)
	if warnings := texts(report, Warn); report.Errors != 0 || len(warnings) != 1 ||
		warnings[0] != "platform/x.sops.yaml: data.token: decodes to base64 text; probably encoded twice" {
		t.Fatalf("errors %d, warnings %q", report.Errors, warnings)
	}
}

func TestASharedTokenMustBeOneValueInBothSecrets(t *testing.T) {
	for _, shared := range sharedValues {
		first, console := shared.Copies[0].File, shared.Copies[1].File
		if got := sharedProblem(shared, map[string]string{first: "a", console: "a"}); got != "" {
			t.Fatalf("%s: equal copies = %q", shared.What, got)
		}
		for _, copies := range []map[string]string{{first: "a", console: "b"}, {first: "a"}, {}} {
			if got := sharedProblem(shared, copies); !strings.Contains(got, shared.What+" must be set and identical") {
				t.Fatalf("%s: %v = %q", shared.What, copies, got)
			}
		}
	}
	report := Check(set(platform(), "platform/console/secret.sops.yaml", "GITHUB_TOKEN", "other"))
	if errors := texts(report, Error); len(errors) != 1 || !strings.HasPrefix(errors[0], "the GitHub dispatch token must be") {
		t.Fatalf("a differing copy = %q", errors)
	}
}

func TestIngestionKeyMustBeOneValueInBothSecrets(t *testing.T) {
	clickstack, otel := ingestionFiles[0], ingestionFiles[1]
	cases := []struct {
		copies map[string]string
		want   string
	}{
		{map[string]string{clickstack: "a", otel: "a"}, ""},
		{map[string]string{clickstack: "a", otel: "b"}, "differs"},
		{map[string]string{clickstack: "a"}, "exactly"},
		{map[string]string{}, "found in none"},
		{map[string]string{clickstack: "a", otel: "a", "platform/x.sops.yaml": "a"}, "exactly"},
	}
	for _, c := range cases {
		got := ingestionProblem(c.copies)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Fatalf("%v = %q, want %q", c.copies, got, c.want)
		}
	}
	// A fixture's copy is not one of the platform's.
	fixture := File{Path: "tests/fixtures/x.sops.yaml", Documents: []map[string]any{secret("ns", "f", map[string]any{IngestionKey: "other"})}}
	if report := Check(append(platform(), fixture)); report.Errors != 0 {
		t.Fatalf("a fixture's ingestion key = %q", texts(report, Error))
	}
}

func TestAFileWithTwoDocumentsIsCheckedDocumentByDocument(t *testing.T) {
	writer := File{Path: "platform/backup/writer.sops.yaml", Documents: []map[string]any{
		secret("flux-system", "backup-writer", map[string]any{"BACKUP_AWS_ACCESS_KEY_ID": "one", "BACKUP_AWS_SECRET_ACCESS_KEY": "two"}),
		secret("observability", "backup-writer", map[string]any{"AWS_ACCESS_KEY_ID": "three", "AWS_SECRET_ACCESS_KEY": ""}),
	}}
	report := Check(append(platform(), writer))
	var summaries []string
	for _, line := range texts(report, OK) {
		if strings.HasPrefix(line, writer.Path+":") {
			summaries = append(summaries, line)
		}
	}
	want := []string{writer.Path + ": flux-system/backup-writer (2 stringData)", writer.Path + ": observability/backup-writer (2 stringData)"}
	if !slices.Equal(summaries, want) {
		t.Fatalf("summaries = %q, want one per document", summaries)
	}
	if errors := texts(report, Error); report.Errors != 1 || errors[0] != writer.Path+": stringData.AWS_SECRET_ACCESS_KEY: empty" {
		t.Fatalf("the second document is checked too: %q", errors)
	}
}

func TestPlaceholdersAndUnreadableFiles(t *testing.T) {
	files := append(platform(),
		File{Path: "apps/a/secret.sops.yaml", Documents: []map[string]any{secret("a", "s", map[string]any{"K": Placeholder})}},
		File{Path: "tests/fixtures/a/secret.sops.yaml", Documents: []map[string]any{secret("a", "s", map[string]any{"K": Placeholder})}},
		File{Path: "apps/b/secret.sops.yaml", Failure: Undecryptable},
		File{Path: "apps/c/secret.sops.yaml", Failure: Unparsable},
		File{Path: "apps/d/secret.sops.yaml", Documents: []map[string]any{nil}},
	)
	report := Check(files)
	want := []string{
		"apps/a/secret.sops.yaml: stringData.K: still the REPLACE_ME placeholder; set it with sops",
		"apps/b/secret.sops.yaml: cannot decrypt",
		"apps/c/secret.sops.yaml: cannot read the decrypted document",
		"apps/d/secret.sops.yaml: no document",
	}
	if got := texts(report, Error); !slices.Equal(got, want) || report.Errors != 4 {
		t.Fatalf("errors = %q", got)
	}
}

// The deliberate changes: what Python stopped on with a traceback is one line here.
func TestWhatPythonCrashedOnIsAnError(t *testing.T) {
	files := append(platform(),
		File{Path: "apps/a/secret.sops.yaml", Documents: []map[string]any{{
			"metadata": map[string]any{"name": "s"}, "data": map[string]any{"K": "not base64!", "N": 5, "L": "YQ==\n"}}}},
		File{Path: "apps/b/secret.sops.yaml", Documents: []map[string]any{{"metadata": map[string]any{}, "stringData": []any{"x"}}}},
		File{Path: "apps/c/secret.sops.yaml", Documents: []map[string]any{{"stringData": map[string]any{"K": "v"}}}},
		File{Path: "apps/d/secret.sops.yaml", Documents: []map[string]any{secret("d", "s", map[string]any{"K": nil, "P": 5432})}},
	)
	report := Check(files)
	want := []string{
		"apps/a/secret.sops.yaml: data.K: not base64",
		"apps/a/secret.sops.yaml: data.L: not base64",
		"apps/a/secret.sops.yaml: data.N: not base64",
		"apps/b/secret.sops.yaml: a document is not a Secret",
		"apps/c/secret.sops.yaml: a document is not a Secret",
		"apps/d/secret.sops.yaml: stringData.K: empty",
	}
	if got := texts(report, Error); !slices.Equal(got, want) {
		t.Fatalf("errors = %q", got)
	}
	if !slices.Contains(texts(report, OK), "apps/a/secret.sops.yaml: -/s (3 data)") {
		t.Fatalf("a namespace that is not set = %q", texts(report, OK))
	}
}

func TestNoLineCarriesAValueOrAFingerprint(t *testing.T) {
	files := set(platform(), "platform/otel/secret.sops.yaml", IngestionKey, "a-private-value")
	files = set(files, "platform/console/secret.sops.yaml", "GITHUB_TOKEN", "another-private-value")
	for _, line := range Check(files).Lines {
		for _, value := range []string{"private-value", digest([]byte("a-private-value")), digest([]byte("ingest")), "ntfy.sh"} {
			if strings.Contains(line.Text, value) {
				t.Fatalf("%q carries %q", line.Text, value)
			}
		}
	}
}

func TestTracked(t *testing.T) {
	got := Tracked([]string{".sops.yaml", "a/.sops.yml", "a/secret.sops.yaml", "README.md", "b/x.sops.yml", "c/sops.yaml"})
	if want := []string{"a/secret.sops.yaml", "b/x.sops.yml"}; !slices.Equal(got, want) {
		t.Fatalf("Tracked = %q", got)
	}
}

package appsource_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/appsource"
	"github.com/samclement/swhurl-platform/v2/adapters/appsource/appsourcetest"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/producer"
)

func write(t *testing.T, root, name string, data []byte) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestRealContract(t *testing.T) {
	appsourcetest.Contract(t, func(t *testing.T) (appsource.Reader, string) {
		root := t.TempDir()
		for _, name := range []string{"swhurl.yaml", "asyncapi.yaml", "api.bin"} {
			data, err := os.ReadFile("testdata/" + name)
			if err != nil {
				t.Fatal(err)
			}
			write(t, root, name, data)
		}
		return appsource.Local{}, root
	})
}

// Frozen before replacement using the original Python producer. Compare the entire label, including base64
// flags bytes; comparing decoded JSON would hide release-identity changes in flags formatting.
func TestPythonProducerGoldens(t *testing.T) {
	data, err := os.ReadFile("testdata/producer.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name  string
		Files map[string]string
		Label string
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			root := t.TempDir()
			for name, data := range c.Files {
				write(t, root, name, []byte(data))
			}
			label, err := orchestration.ImageDeclaration(appsource.Local{}, root)
			if err != nil || label+"\n" != c.Label {
				t.Fatalf("label = %s\nerror = %v\nwant = %s", label, err, c.Label)
			}
		})
	}
}
func TestStrictYAML(t *testing.T) {
	for _, source := range []string{"[a, b]\n", "version: 1\n---\nkind: worker\n", "kind: !custom worker\n", "!custom {version: 1, kind: worker}", "flags: !custom []\n", "autoDeploy: !!bool invalid\n", "kind: [\n", string([]byte{255})} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "swhurl.yaml", []byte(source))
			o, err := (appsource.Local{}).Declaration(root)
			if err == nil || o != nil || !strings.Contains(err.Error(), "swhurl.yaml") {
				t.Fatalf("%#v, %v", o, err)
			}
		})
	}
}
func TestExplicitlyNamedSymlinksFollowOriginalFileReads(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	write(t, outside, "document", []byte("raw outside bytes\r\n"))
	if err := os.Symlink(filepath.Join(outside, "document"), filepath.Join(root, "api")); err != nil {
		t.Fatal(err)
	}
	if data, err := (appsource.Local{}).API(root, "api"); err != nil || string(data) != "raw outside bytes\r\n" {
		t.Fatalf("named external symlink: %q, %v", data, err)
	}
	write(t, root, "swhurl.yaml", []byte("version: 1\nkind: web\nhealthPath: /h\nprovides: {api: /api, apiFile: api}\n"))
	// Frozen from HEAD's producer following this same externally targeted symlink.
	want := "platform.swhurl.com/declaration={\"schemaVersion\":2,\"runtime\":{\"kind\":\"web\",\"uid\":65532,\"port\":8080,\"healthPath\":\"/h\"},\"memoryMiB\":128,\"apiPath\":\"/api\",\"documents\":{\"api\":\"cmF3IG91dHNpZGUgYnl0ZXMNCg==\"}}"
	if label, err := orchestration.ImageDeclaration(appsource.Local{}, root); err != nil || label != want {
		t.Fatal(label, err)
	}
	if err := os.Remove(filepath.Join(root, "swhurl.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(os.DevNull, filepath.Join(root, "empty")); err != nil {
		t.Fatal(err)
	}
	if data, err := (appsource.Local{}).API(root, "empty"); err != nil || len(data) != 0 {
		t.Fatalf("named non-regular file: %q, %v", data, err)
	}
	write(t, outside, "source", []byte("version: 1\nkind: worker\n"))
	if err := os.Symlink(filepath.Join(outside, "source"), filepath.Join(root, "swhurl.yaml")); err != nil {
		t.Fatal(err)
	}
	if label, err := orchestration.ImageDeclaration(appsource.Local{}, root); err != nil || label == "" {
		t.Fatal(label, err)
	}
}
func TestPythonProducerRefusals(t *testing.T) {
	data, err := os.ReadFile("testdata/refusals.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name  string
		Files map[string]string
		Says  string
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			root := t.TempDir()
			for name, data := range c.Files {
				write(t, root, name, []byte(data))
			}
			label, err := orchestration.ImageDeclaration(appsource.Local{}, root)
			if err == nil || label != "" || !strings.Contains(err.Error(), c.Says) {
				t.Fatalf("label %s, err %v; want original refusal %s", label, err, c.Says)
			}
		})
	}
}
func TestLargeSourceCommentsDoNotLimitAFittingLabel(t *testing.T) {
	root := t.TempDir()
	write(t, root, "swhurl.yaml", []byte("# "+strings.Repeat("x", 1024*1024)+"\nversion: 1\nkind: worker\n"))
	label, err := orchestration.ImageDeclaration(appsource.Local{}, root)
	if err != nil || label != `platform.swhurl.com/declaration={"schemaVersion":2,"runtime":{"kind":"worker","uid":65532},"memoryMiB":128}` {
		t.Fatal(label, err)
	}
}
func TestOversizedDocumentNamesRoleAndSize(t *testing.T) {
	root := t.TempDir()
	write(t, root, "swhurl.yaml", []byte("version: 1\nkind: web\nhealthPath: /h\nprovides: {api: /api, apiFile: api.bin}\n"))
	write(t, root, "api.bin", []byte(strings.Repeat("x", 1024*1024)))
	label, err := orchestration.ImageDeclaration(appsource.Local{}, root)
	if err == nil || label != "" || !strings.Contains(err.Error(), "over the 122880-byte cap") || !strings.Contains(err.Error(), "api (1048576 bytes)") {
		t.Fatal(label, err)
	}
}

func TestProducerFailuresNameSourceAndDocuments(t *testing.T) {
	for _, c := range []struct{ source, events, says string }{
		{"version: 1\nkind: web\nhealthPath: /h\nprovides: {api: /api}\n", "", "stated together or not at all"},
		{"version: 1\nkind: web\nhealthPath: /h\nprovides: {apiFile: api.json}\n", "", "stated together or not at all"},
		{"version: 1\nkind: web\nhealthPath: /h\nprovides: {api: /api, apiFile: api.json}\n", "", "api.json"},
		{"version: 1\nkind: worker\nprovides: {eventsFile: events.yaml}\n", "", "events.yaml"},
		{"version: 1\nkind: worker\nprovides: {eventsFile: events.yaml}\n", "asyncapi: 2.6.0\n", "AsyncAPI 3"},
		{"version: 1\nkind: worker\nprovides: {eventsFile: events.yaml}\n", "asyncapi: 3.0.0\noperations: {publish: {action: send}}\n", "channel must reference"},
	} {
		root := t.TempDir()
		write(t, root, "swhurl.yaml", []byte(c.source))
		if c.events != "" {
			write(t, root, "events.yaml", []byte(c.events))
		}
		label, err := orchestration.ImageDeclaration(appsource.Local{}, root)
		if err == nil || label != "" || !strings.Contains(err.Error(), c.says) {
			t.Fatalf("label %s, err %v; want %s", label, err, c.says)
		}
	}
	root := t.TempDir()
	if label, err := orchestration.ImageDeclaration(appsource.Local{}, root); err == nil || label != "" || !strings.Contains(err.Error(), "swhurl.yaml") {
		t.Fatal(label, err)
	}
}
func TestSourceNumericAndBooleanTypes(t *testing.T) {
	for _, c := range []struct {
		value    string
		accepted bool
	}{{"1e3", false}, {"0o10", false}, {"1.0e+3", true}, {"012", true}, {"1:20", true}, {"true", false}, {"tRuE", false}, {".inf", false}, {".nan", false}, {"1.0e-999", true}} {
		root := t.TempDir()
		write(t, root, "swhurl.yaml", []byte("version: 1\nkind: worker\nflags:\n  count:\n    type: number\n    default: "+c.value+"\n"))
		label, err := orchestration.ImageDeclaration(appsource.Local{}, root)
		if (err == nil) != c.accepted {
			t.Fatalf("%s: %s, %v", c.value, label, err)
		}
	}
	root := t.TempDir()
	write(t, root, "swhurl.yaml", []byte("version: 1\nkind: web\nhealthPath: /h\nuid: 1234567890123456789012345\nresources: {memory: 1234567890123456789012345Gi}\n"))
	label, err := orchestration.ImageDeclaration(appsource.Local{}, root)
	if err != nil || !strings.Contains(label, `"uid":1234567890123456789012345`) || !strings.Contains(label, `"memoryMiB":1264197519486419751948641280`) {
		t.Fatal(label, err)
	}
}
func TestCapNamesDocumentSizesAndCountsUnicodeBytes(t *testing.T) {
	root := t.TempDir()
	source := "version: 1\nkind: web\nhealthPath: /h\nprovides: {api: /api, apiFile: api.bin}\ndescription: "
	write(t, root, "swhurl.yaml", []byte(source+strings.Repeat("x", 200)+"\n"))
	// Choose a document that fits with ASCII but not with a valid 200-character multibyte description.
	data := []byte(strings.Repeat("x", 91600))
	write(t, root, "api.bin", data)
	if _, err := orchestration.ImageDeclaration(appsource.Local{}, root); err != nil {
		t.Fatal(err)
	}
	write(t, root, "swhurl.yaml", []byte(source+strings.Repeat("😀", 200)+"\n"))
	if label, err := orchestration.ImageDeclaration(appsource.Local{}, root); err == nil || label != "" || !strings.Contains(err.Error(), "over the") || !strings.Contains(err.Error(), "api (91600 bytes)") {
		t.Fatal(label, err)
	}
}

func TestOverflowFloatBoundRetainsPythonInfinity(t *testing.T) {
	root := t.TempDir()
	write(t, root, "swhurl.yaml", []byte("version: 1\nkind: worker\nflags:\n  amount:\n    type: number\n    default: 1.0\n    max: 1.0e+999\n"))
	label, err := orchestration.ImageDeclaration(appsource.Local{}, root)
	if err != nil || label == "" {
		t.Fatal(label, err)
	}
}

func TestCyclicAliasesAndInvalidMergesFailSafely(t *testing.T) {
	for _, source := range []string{
		"version: 1\nkind: worker\nflags: &flags {sample: *flags}\n",
		"version: 1\nkind: worker\nflags: &flags {<<: *flags}\n",
		"version: 1\nkind: worker\nflags: &flags [*flags]\n",
		"version: 1\nkind: worker\nflags: {<<: 1}\n",
		"version: 1\nkind: worker\nflags: {<<: [{}, scalar]}\n",
	} {
		root := t.TempDir()
		write(t, root, "swhurl.yaml", []byte(source))
		label, err := orchestration.ImageDeclaration(appsource.Local{}, root)
		if err == nil || label != "" || !strings.Contains(err.Error(), "swhurl.yaml") {
			t.Fatal(label, err)
		}
	}
}
func TestBooleanEventAddressesRemainInvalidWithAliases(t *testing.T) {
	for _, address := range []string{"yes", "true", "&address yes", "&address true"} {
		root := t.TempDir()
		write(t, root, "swhurl.yaml", []byte("version: 1\nkind: worker\nprovides: {eventsFile: events.yaml}\n"))
		events := "asyncapi: 3.0.0\nchannels:\n  original: {address: " + address + "}\noperations:\n  send: {action: send, channel: {$ref: '#/channels/original'}}\n"
		if strings.HasPrefix(address, "&") {
			events = "asyncapi: 3.0.0\nchannels:\n  original: {address: " + address + "}\n  alias: {address: *address}\noperations:\n  send: {action: send, channel: {$ref: '#/channels/alias'}}\n"
		}
		write(t, root, "events.yaml", []byte(events))
		label, err := orchestration.ImageDeclaration(appsource.Local{}, root)
		if err == nil || label != "" || !strings.Contains(err.Error(), "invalid event address True") {
			t.Fatal(address, label, err)
		}
	}
}

func TestEventMappingKeysRetainIdentity(t *testing.T) {
	root := t.TempDir()
	write(t, root, "events.yaml", []byte("asyncapi: 3.0.0\nchannels:\n  123: {address: numeric}\n  '123': {address: text}\n"))
	doc, _, err := (appsource.Local{}).Events(root, "events.yaml")
	if err != nil {
		t.Fatal(err)
	}
	channels := doc.Get("channels").(producer.Object)
	if len(channels) != 2 || channels[0].Name != int64(123) || channels[1].Name != "123" {
		t.Fatalf("key identities: %#v", channels)
	}
	write(t, root, "events.yaml", []byte("asyncapi: 3.0.0\nchannels:\n  true: {address: first}\n  1: {address: replaced}\n  '1': {address: text}\n"))
	doc, _, err = (appsource.Local{}).Events(root, "events.yaml")
	if err != nil {
		t.Fatal(err)
	}
	channels = doc.Get("channels").(producer.Object)
	if len(channels) != 2 || channels[0].Name != true || channels[1].Name != "1" || channels[0].Value.(producer.Object).Get("address") != "replaced" {
		t.Fatalf("Python numeric key equality: %#v", channels)
	}
}
func TestNonStringSourceNamesRemainDomainRefusals(t *testing.T) {
	for _, source := range []string{"version: 1\nkind: worker\n1: extra\n", "version: 1\nkind: worker\nflags: {1: {type: boolean, default: false}}\n", "version: 1\nkind: worker\nflags: {on: {type: boolean, default: false}}\n"} {
		root := t.TempDir()
		write(t, root, "swhurl.yaml", []byte(source))
		doc, err := (appsource.Local{}).Declaration(root)
		if err != nil {
			t.Fatalf("adapter must retain keys for domain validation: %v", err)
		}
		if _, err := producer.Parse(doc); err == nil || !strings.Contains(err.Error(), "swhurl.yaml") {
			t.Fatalf("source domain accepted invalid names: %#v, %v", doc, err)
		}
	}
}
func TestUnhashableYAMLKeysAreRefused(t *testing.T) {
	root := t.TempDir()
	write(t, root, "events.yaml", []byte("asyncapi: 3.0.0\ncomponents: {? [a, b]: value}\n"))
	if doc, data, err := (appsource.Local{}).Events(root, "events.yaml"); err == nil || doc != nil || data != nil {
		t.Fatal(doc, data, err)
	}
}

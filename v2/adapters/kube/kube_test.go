package kube_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

const endpoint = "https://api.swhurl.test:6443"

// document is one recorded API document from testdata.
func document(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("testdata/%s is not JSON", name)
	}
	return data
}

// refOf is the reference to an object, read from the recorded document itself: the API version and
// kind the adapter states for it must be the ones its own document carries.
func refOf(t *testing.T, data []byte) observe.Ref {
	t.Helper()
	var document struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return observe.Ref{APIVersion: document.APIVersion, Kind: document.Kind,
		Namespace: document.Metadata.Namespace, Name: document.Metadata.Name}
}

// itemsOf is a list document's items, as raw documents.
func itemsOf(t *testing.T, list []byte) [][]byte {
	t.Helper()
	var document struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(list, &document); err != nil {
		t.Fatal(err)
	}
	items := make([][]byte, 0, len(document.Items))
	for _, item := range document.Items {
		var compact bytes.Buffer
		if err := json.Compact(&compact, item); err != nil {
			t.Fatal(err)
		}
		items = append(items, compact.Bytes())
	}
	return items
}

// answers is the contract's scenario from the recorded documents: the Deployment is the object that is
// there; three copies of its reference name one that is absent, one that cannot be read and one whose
// answer cannot be used.
func answers(t *testing.T) kubetest.Answers {
	t.Helper()
	workload := document(t, "deployment.json")
	return kubetest.Answers{
		Object:     refOf(t, workload),
		Document:   workload,
		Missing:    named(t, workload, "missing"),
		Failed:     named(t, workload, "failed"),
		Malformed:  named(t, workload, "malformed"),
		Mismatched: named(t, workload, "mismatched"),
		Pods:       observe.Ref{APIVersion: "v1", Kind: "Pod", Namespace: refOf(t, workload).Namespace},
		Selector:   "app.kubernetes.io/instance=hello,app.kubernetes.io/controller=main",
		Items:      itemsOf(t, document(t, "pods.json")),
	}
}

// named is the same kind and namespace as a document, with another object's name.
func named(t *testing.T, data []byte, name string) observe.Ref {
	t.Helper()
	ref := refOf(t, data)
	ref.Name = name
	return ref
}

// apiServer answers requests from the recorded documents: the real adapter's URL building, response
// handling and copy ownership are exercised with no listener, no credential and no cluster.
type apiServer struct {
	answers   kubetest.Answers
	documents map[string][]byte // "<collection>/<name>" to the recorded document
	requests  []*http.Request
	status    int    // when set, every answer has this status
	body      []byte // when set, every answer has these bytes, with status 200
	failure   error  // when set, every request ends with it
	redirect  string // when set, every request is answered with a 302 to it
	broken    error  // when set, the answer's body fails when it is read
}

// brokenBody fails when it is read, as a connection that drops mid-answer does.
type brokenBody struct{ err error }

func (b brokenBody) Read([]byte) (int, error) { return 0, b.err }
func (b brokenBody) Close() error             { return nil }

func (s *apiServer) RoundTrip(request *http.Request) (*http.Response, error) {
	s.requests = append(s.requests, request)
	if s.failure != nil {
		return nil, s.failure
	}
	if s.redirect != "" {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {s.redirect}},
			Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	}
	if s.broken != nil {
		return &http.Response{StatusCode: http.StatusOK, Body: brokenBody{s.broken},
			Header: http.Header{}, Request: request}, nil
	}
	status, body := s.answer(request)
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)),
		Header: http.Header{}, Request: request}, nil
}

func (s *apiServer) answer(request *http.Request) (int, []byte) {
	segments := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	last := segments[len(segments)-1]
	pair := ""
	if len(segments) > 1 {
		pair = segments[len(segments)-2] + "/" + last
	}
	switch {
	case s.body != nil:
		return http.StatusOK, s.body
	case s.status != 0:
		return s.status, nil
	case strings.HasSuffix(pair, "/"+s.answers.Missing.Name):
		return http.StatusNotFound, nil
	case strings.HasSuffix(pair, "/"+s.answers.Failed.Name):
		return http.StatusForbidden, nil
	case strings.HasSuffix(pair, "/"+s.answers.Malformed.Name):
		return http.StatusOK, []byte("not json")
	case strings.HasSuffix(pair, "/"+s.answers.Mismatched.Name):
		return http.StatusOK, s.answers.Document
	case s.documents[pair] != nil:
		return http.StatusOK, s.documents[pair]
	case last == "pods" && request.URL.Query().Get("labelSelector") == s.answers.Selector:
		return http.StatusOK, listDocument(s.answers.Items)
	}
	return http.StatusInternalServerError, nil
}

// listDocument is a PodList document around the scenario's items.
func listDocument(items [][]byte) []byte {
	// The API server writes an empty array, never null, for a list with no items.
	document := struct {
		Items []json.RawMessage `json:"items"`
	}{Items: make([]json.RawMessage, 0, len(items))}
	for _, item := range items {
		document.Items = append(document.Items, item)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return encoded
}

// fixtures are the recorded documents, by the collection and name the API server addresses them by.
func fixtures(t *testing.T) map[string][]byte {
	t.Helper()
	return map[string][]byte{
		"deployments/hello-main":           document(t, "deployment.json"),
		"kustomizations/app-hello-staging": document(t, "kustomization.json"),
		"helmreleases/hello":               document(t, "helmrelease.json"),
	}
}

// readerWith returns a reader over the scenario's answers, and the server that answered it.
func readerWith(t *testing.T, scenario kubetest.Answers) (kube.Reader, *apiServer) {
	t.Helper()
	documents := fixtures(t)
	documents["deployments/"+scenario.Object.Name] = scenario.Document
	server := &apiServer{answers: scenario, documents: documents}
	reader, err := kube.New(endpoint, &http.Client{Transport: server}, "")
	if err != nil {
		t.Fatal(err)
	}
	return reader, server
}

func TestContract(t *testing.T) {
	kubetest.Contract(t, answers(t), func(t *testing.T, scenario kubetest.Answers) kube.Reader {
		reader, _ := readerWith(t, scenario)
		return reader
	})
}

// The path and query are what the API server documents for each supported kind: the kinds' group and
// version are stated, not discovered.
func TestPathsAreStatedPerKind(t *testing.T) {
	scenario := answers(t)
	reader, server := readerWith(t, scenario)
	unit := refOf(t, document(t, "kustomization.json"))
	release := refOf(t, document(t, "helmrelease.json"))
	if _, err := reader.Read(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(context.Background(), release); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(context.Background(), scenario.Object); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.List(context.Background(), scenario.Pods, scenario.Selector); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/flux-system/kustomizations/app-hello-staging",
		"/apis/helm.toolkit.fluxcd.io/v2/namespaces/hello-staging/helmreleases/hello",
		"/apis/apps/v1/namespaces/hello-staging/deployments/hello-main",
		"/api/v1/namespaces/hello-staging/pods",
	}
	if len(server.requests) != len(want) {
		t.Fatalf("%d requests for %d refs", len(server.requests), len(want))
	}
	for i, request := range server.requests {
		if request.URL.Path != want[i] {
			t.Errorf("request %d is %s, want %s", i, request.URL.Path, want[i])
		}
		if request.Method != http.MethodGet {
			t.Errorf("request %d is %s", i, request.Method)
		}
	}
	if selector := server.requests[3].URL.Query().Get("labelSelector"); selector != scenario.Selector {
		t.Errorf("the list was sent with selector %q", selector)
	}
}

// A bearer token travels with the request; nothing else about it appears in the evidence.
func TestBearerTokenIsSent(t *testing.T) {
	scenario := answers(t)
	reader, server := readerWith(t, scenario)
	endpoint, err := kube.New(endpoint, &http.Client{Transport: server}, "an-opaque-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.Read(context.Background(), scenario.Object); err != nil {
		t.Fatal(err)
	}
	if got := server.requests[0].Header.Get("Authorization"); got != "Bearer an-opaque-token" {
		t.Fatalf("authorization header %q", got)
	}
	if got := server.requests[0].Header.Get("Accept"); got != "application/json" {
		t.Fatalf("accept header %q", got)
	}
	if _, err := reader.Read(context.Background(), scenario.Object); err != nil {
		t.Fatal(err)
	}
	if got := server.requests[1].Header.Get("Authorization"); got != "" {
		t.Fatalf("a reader without a token sent %q", got)
	}
}

// A status the server answers with is evidence about the object, and only 404 is absence.
func TestServerAnswersBecomeEvidence(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		read, err := readWithStatus(t, status)
		if err != nil || read.Status != observe.Unavailable || read.Reason != observe.ReasonForbidden {
			t.Errorf("status %d: %+v %v", status, read, err)
		}
	}
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		read, err := readWithStatus(t, status)
		if err != nil || read.Status != observe.Unavailable || read.Reason != observe.ReasonStatus {
			t.Errorf("status %d: %+v %v", status, read, err)
		}
	}
}

func readWithStatus(t *testing.T, status int) (observe.Read, error) {
	t.Helper()
	scenario := answers(t)
	reader, server := readerWith(t, scenario)
	server.status = status
	return reader.Read(context.Background(), scenario.Object)
}

// A request that never reached an answer, an answer too large to be one object, an empty body and a
// body that fails mid-read are read failures: each is unavailable with its own reason, never present
// and never absent.
func TestFailuresWithoutAnAnswer(t *testing.T) {
	for _, tt := range []struct {
		name    string
		failure error
		body    []byte
		reason  string
	}{
		{"a timeout", context.DeadlineExceeded, nil, observe.ReasonTimedOut},
		{"a transport failure", errors.New("connection reset"), nil, observe.ReasonTransport},
		{"an oversized answer", nil, make([]byte, 2<<20), observe.ReasonTooLarge},
		{"an empty answer", nil, []byte{}, observe.ReasonMalformed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scenario := answers(t)
			reader, server := readerWith(t, scenario)
			server.failure, server.body = tt.failure, tt.body
			read, err := reader.Read(context.Background(), scenario.Object)
			if err != nil || read.Status != observe.Unavailable || read.Reason != tt.reason || len(read.Object) != 0 {
				t.Fatalf("%+v %v", read, err)
			}
		})
	}
}

// A deadline while the body is being read is a timeout, not a transport failure.
func TestBodyReadFailureIsReported(t *testing.T) {
	scenario := answers(t)
	reader, server := readerWith(t, scenario)
	server.broken = context.DeadlineExceeded
	if read, err := reader.Read(context.Background(), scenario.Object); err != nil || read.Reason != observe.ReasonTimedOut {
		t.Fatalf("a body that timed out: %+v %v", read, err)
	}
}

// A redirect is neither followed nor a place to send the token: the 3xx is the answer, so a 404 behind
// it cannot become absence and the token stays at the endpoint asked.
func TestRedirectsAreNotFollowed(t *testing.T) {
	scenario := answers(t)
	server := &apiServer{answers: scenario, documents: fixtures(t), redirect: "http://plain.example:8080/elsewhere"}
	reader, err := kube.New(endpoint, &http.Client{Transport: server}, "an-opaque-token")
	if err != nil {
		t.Fatal(err)
	}
	read, err := reader.Read(context.Background(), scenario.Object)
	if err != nil || read.Status != observe.Unavailable || read.Reason != observe.ReasonStatus {
		t.Fatalf("a redirect: %+v %v", read, err)
	}
	if len(server.requests) != 1 {
		t.Fatalf("%d requests: a redirect was followed", len(server.requests))
	}
	if got := server.requests[0].Header.Get("Authorization"); got != "Bearer an-opaque-token" {
		t.Fatalf("the first request carried %q", got)
	}
}

// A list body without items is not a list, so it is a failed read rather than an empty one.
func TestAListNeedsItems(t *testing.T) {
	scenario := answers(t)
	reader, server := readerWith(t, scenario)
	server.body = []byte(`{"kind":"Status","apiVersion":"v1","status":"Failure"}`)
	list, err := reader.List(context.Background(), scenario.Pods, scenario.Selector)
	if err != nil || list.Status != observe.Unavailable || list.Reason != observe.ReasonMalformed || len(list.Items) != 0 {
		t.Fatalf("a body without items: %+v %v", list, err)
	}
}

// A reference cannot reshape the request path or name something the API could not have.
func TestInvalidReferencesAreRefused(t *testing.T) {
	scenario := answers(t)
	reader, _ := readerWith(t, scenario)
	for _, tt := range []struct{ name, reason string }{
		{"..", "invalid_name"},
		{"a/b", "invalid_name"},
		{"Hello", "invalid_name"},
		{"", "name_required"},
	} {
		ref := scenario.Object
		ref.Name = tt.name
		if _, err := reader.Read(context.Background(), ref); !refused(err, tt.reason) {
			t.Errorf("name %q: %v", tt.name, err)
		}
	}
	ref := scenario.Object
	ref.Namespace = "../flux-system"
	if _, err := reader.Read(context.Background(), ref); !refused(err, "invalid_namespace") {
		t.Errorf("namespace: %v", err)
	}
}

// refused reports whether an error is the adapter's refusal with this reason.
func refused(err error, reason string) bool {
	var failure *kube.Error
	return errors.As(err, &failure) && failure.Code == kube.RefusedRequest && failure.Reason == reason
}

// A collection read has no absent form, and an empty successful list is present with no items.
func TestEmptyListIsPresent(t *testing.T) {
	scenario := answers(t)
	scenario.Items = nil
	reader, _ := readerWith(t, scenario)
	list, err := reader.List(context.Background(), scenario.Pods, scenario.Selector)
	if err != nil || list.Status != observe.Present || len(list.Items) != 0 {
		t.Fatalf("an empty list: %+v %v", list, err)
	}
	scenario = answers(t)
	reader, server := readerWith(t, scenario)
	server.status = http.StatusNotFound
	list, err = reader.List(context.Background(), scenario.Pods, scenario.Selector)
	if err != nil || list.Status != observe.Unavailable || list.Reason != observe.ReasonStatus {
		t.Fatalf("a collection that is not there: %+v %v", list, err)
	}
}

// The adapter refuses an endpoint it would have to reach in the clear, and a client it does not have.
func TestNewRefusals(t *testing.T) {
	client := &http.Client{}
	for _, tt := range []struct {
		endpoint string
		client   *http.Client
		reason   string
	}{
		{"http://api.swhurl.test:6443", client, "https_required"},
		{"api.swhurl.test:6443", client, "not_a_url"},
		{endpoint, nil, "required"},
	} {
		_, err := kube.New(tt.endpoint, tt.client, "")
		var refusal *kube.Error
		if !errors.As(err, &refusal) || refusal.Code != kube.RefusedRequest || refusal.Reason != tt.reason {
			t.Errorf("endpoint %q: %v", tt.endpoint, err)
		}
	}
}

// Client refuses credential material it cannot use, and keeps what it can.
func TestClient(t *testing.T) {
	if _, err := kube.Client(kube.Credentials{CA: []byte("not a certificate")}); !hasCode(err, kube.BadCredentials) {
		t.Errorf("an unusable CA: %v", err)
	}
	if _, err := kube.Client(kube.Credentials{Certificate: []byte("only a certificate")}); !hasCode(err, kube.BadCredentials) {
		t.Errorf("a certificate without its key: %v", err)
	}
	certificate, key := testCertificate(t)
	client, err := kube.Client(kube.Credentials{Certificate: certificate, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	configuration := client.Transport.(*http.Transport).TLSClientConfig
	if len(configuration.Certificates) != 1 || configuration.MinVersion != 0x0303 {
		t.Fatalf("client certificate configuration: %+v", configuration)
	}
	plain, err := kube.Client(kube.Credentials{})
	if err != nil || plain.Transport.(*http.Transport).TLSClientConfig.RootCAs != nil {
		t.Fatalf("default credentials: %v", err)
	}
}

func hasCode(err error, code string) bool {
	var failure *kube.Error
	return errors.As(err, &failure) && failure.Code == code
}

// testCertificate is a fresh self-signed certificate and key, in PEM.
func testCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "swhurl-v2-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	private := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encoded})
	return certificate, private
}

// target is what v2 intended for the recorded instance, with each object's reference taken from its own
// document and the commit from the unit's applied revision.
func target(t *testing.T, scenario kubetest.Answers) observe.Target {
	t.Helper()
	return observe.Target{
		Namespace: "hello-staging",
		Unit:      refOf(t, document(t, "kustomization.json")),
		Release:   refOf(t, document(t, "helmrelease.json")),
		Workload:  scenario.Object,
		Pods:      scenario.Pods,
		Selector:  scenario.Selector,
		Container: "main",
		Digest:    "sha256:65e3e85dbaed8ba248841d9d58a899b6197106c23cb0ff1a132b7bfe0547e4c0",
		Replicas:  1,
		Revision:  "9f1c0e2d34b7a5c86e4f0d2b9a17c3e5d8b6f04a",
	}
}

// observed is one whole observation of the recorded instance: the recorded answers reach the real
// adapter, and the pure projection decides what they mean.
func observed(t *testing.T, scenario kubetest.Answers) observe.Report {
	t.Helper()
	reader, server := readerWith(t, scenario)
	ctx := context.Background()
	target := target(t, scenario)
	unit, err := reader.Read(ctx, target.Unit)
	if err != nil {
		t.Fatal(err)
	}
	release, err := reader.Read(ctx, target.Release)
	if err != nil {
		t.Fatal(err)
	}
	workload, err := reader.Read(ctx, target.Workload)
	if err != nil {
		t.Fatal(err)
	}
	pods, err := reader.List(ctx, target.Pods, target.Selector)
	if err != nil {
		t.Fatal(err)
	}
	if len(server.requests) != 4 {
		t.Fatalf("%d requests for one observation", len(server.requests))
	}
	report, err := observe.Explain(target, observe.Evidence{Source: "api.swhurl.test", ObservedAt: "2026-10-25T09:14:00Z",
		Objects: []observe.Read{unit, release, workload}, Pods: []observe.List{pods}})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// The recorded instance is the intended target running: every fact is established and positive.
func TestCompositionReportsTheRecordedInstanceRunning(t *testing.T) {
	report := observed(t, answers(t))
	if report.Verdict != observe.IsRunning {
		t.Fatalf("verdict %s, findings %+v", report.Verdict, report.Findings)
	}
	if len(report.Findings) != 7 || len(report.Unassessed) != 0 || len(report.Assessed) != 7 {
		t.Fatalf("findings %+v, assessed %v, unassessed %v", report.Findings, report.Assessed, report.Unassessed)
	}
}

// A read the source does not answer leaves the answer unknown rather than running or absent: the same
// instance, with the pods unreadable, is not claimed either way.
func TestCompositionLeavesUnreadEvidenceUnknown(t *testing.T) {
	scenario := answers(t)
	reader, server := readerWith(t, scenario)
	target := target(t, scenario)
	ctx := context.Background()
	unit, _ := reader.Read(ctx, target.Unit)
	release, _ := reader.Read(ctx, target.Release)
	workload, _ := reader.Read(ctx, target.Workload)
	server.status = http.StatusForbidden
	pods, err := reader.List(ctx, target.Pods, target.Selector)
	if err != nil {
		t.Fatal(err)
	}
	report, err := observe.Explain(target, observe.Evidence{Objects: []observe.Read{unit, release, workload}, Pods: []observe.List{pods}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != observe.Unknown {
		t.Fatalf("verdict %s, findings %+v", report.Verdict, report.Findings)
	}
	for _, finding := range report.Findings {
		if finding.Field == "image" && (finding.Outcome != observe.NotAssessed || finding.Reason != observe.ReasonPodsDown) {
			t.Fatalf("an unreadable pod list: %+v", finding)
		}
	}
}

// A pod running another digest is a difference, not readiness: the workload can be Available while a
// rollout replaces it.
func TestCompositionSeesAnotherDigestRunning(t *testing.T) {
	scenario := answers(t)
	scenario.Items = [][]byte{[]byte(strings.Replace(string(scenario.Items[0]),
		"sha256:65e3e85dbaed8ba248841d9d58a899b6197106c23cb0ff1a132b7bfe0547e4c0",
		"sha256:"+strings.Repeat("a", 64), -1))}
	report := observed(t, scenario)
	if report.Verdict != observe.IsNotRunning {
		t.Fatalf("verdict %s, findings %+v", report.Verdict, report.Findings)
	}
	for _, finding := range report.Findings {
		if finding.Field == "image" && (finding.Outcome != observe.Different || finding.Reason != observe.ReasonDigest) {
			t.Fatalf("another digest running: %+v", finding)
		}
	}
}

package s3

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// AWS's published example for signing a GET Object, reproduced field for field: the canonical request, the
// string to sign, the signing key and the signature. If this disagrees, nothing S3 answers can be trusted.
func TestSignMatchesThePublishedVector(t *testing.T) {
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	request, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Range", "bytes=0-9")
	request.Header.Set("x-amz-content-sha256", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	sign(request, "us-east-1", Credentials{AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}, at)

	authorization := request.Header.Get("Authorization")
	for _, want := range []string{
		"AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request",
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date",
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
	} {
		if !strings.Contains(authorization, want) {
			t.Errorf("authorization lacks %q: %s", want, authorization)
		}
	}
	if got := request.Header.Get("x-amz-date"); got != "20130524T000000Z" {
		t.Fatalf("x-amz-date %q", got)
	}
}

// AWS's published example for a presigned GET: the scope, the expiry and the signature. A restore pod reads one
// archive with this URL, so a wrong canonicalisation would be invisible until a pod could not fetch its backup.
func TestPresignMatchesThePublishedVector(t *testing.T) {
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	got, err := url.Parse(presignedURL("examplebucket.s3.amazonaws.com", "/test.txt", "us-east-1",
		Credentials{AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		24*time.Hour, at))
	if err != nil || got.Scheme != "https" || got.Host != "examplebucket.s3.amazonaws.com" || got.Path != "/test.txt" {
		t.Fatalf("presigned URL %v, %v", got, err)
	}
	want := url.Values{"X-Amz-Algorithm": {"AWS4-HMAC-SHA256"},
		"X-Amz-Credential": {"AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request"},
		"X-Amz-Date":       {"20130524T000000Z"}, "X-Amz-Expires": {"86400"}, "X-Amz-SignedHeaders": {"host"},
		"X-Amz-Signature": {"aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"}}
	if !reflect.DeepEqual(got.Query(), want) {
		t.Fatalf("presigned query\n%v\nwant\n%v", got.Query(), want)
	}
}

// A session token is a signed query parameter, so the URL works with temporary credentials and no header.
func TestPresignCarriesTheSessionToken(t *testing.T) {
	address := presignedURL("bucket.s3.eu-west-2.amazonaws.com", "/a%20b/x.db.age", "eu-west-2",
		Credentials{AccessKey: "AKIA", SecretKey: "secret", SessionToken: "token"}, time.Hour,
		time.Date(2026, 10, 27, 3, 4, 5, 0, time.UTC))
	if !strings.Contains(address, "X-Amz-Security-Token=token") || !strings.Contains(address, "/a%20b/x.db.age?") {
		t.Fatalf("presigned URL %q", address)
	}
}

// A session token travels signed, as S3 requires, and an unsigned payload is stated when the caller did not.
func TestSignIncludesTheSessionToken(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://bucket.s3.eu-west-2.amazonaws.com/?list-type=2&prefix=a%2Fb%2F", nil)
	if err != nil {
		t.Fatal(err)
	}
	sign(request, "eu-west-2", Credentials{AccessKey: "AKIA", SecretKey: "secret", SessionToken: "token"}, time.Now())
	authorization := request.Header.Get("Authorization")
	if !strings.Contains(authorization, "x-amz-security-token") {
		t.Fatalf("the token is not signed: %s", authorization)
	}
	if got := request.Header.Get("x-amz-content-sha256"); got != "UNSIGNED-PAYLOAD" {
		t.Fatalf("payload %q", got)
	}
}

// A key with characters S3 escapes is signed as the request carries it, escaped once: the signatures the
// hand-written signer gave this key (it restored an archive live on 27 October 2026), pinned so a doubled
// escape cannot pass.
func TestAnEscapedKeyIsSignedOnce(t *testing.T) {
	at := time.Date(2026, 10, 27, 3, 4, 5, 0, time.UTC)
	credentials := Credentials{AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	path := "/app-sqlite/hello%20staging/x%2By.db.age"
	request, err := http.NewRequest(http.MethodGet, "https://bucket.s3.eu-west-2.amazonaws.com"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	sign(request, "eu-west-2", credentials, at)
	if want := "Signature=03d73df308a6b5cbe5069b4ff460da8b8fe47d0d0b5f83a2a3802d5b9b52aa52"; !strings.HasSuffix(
		request.Header.Get("Authorization"), want) {
		t.Errorf("signed header %s, want %s", request.Header.Get("Authorization"), want)
	}
	presigned, err := url.Parse(presignedURL("bucket.s3.eu-west-2.amazonaws.com", path, "eu-west-2", credentials,
		time.Hour, at))
	if err != nil || presigned.EscapedPath() != path ||
		presigned.Query().Get("X-Amz-Signature") != "7b232a06684e14a4ccfbbdd0dd30069bb957a60f6b0a0dfb47762a8776707930" {
		t.Errorf("presigned %v, %v", presigned, err)
	}
}

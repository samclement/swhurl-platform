package s3

import (
	"net/http"
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

// The canonical query is sorted and encoded once, whichever way the caller wrote it.
func TestCanonicalQueryIsSortedAndEncodedOnce(t *testing.T) {
	got := canonicalQuery("prefix=a%2Fb%2F&list-type=2&continuation-token=x%20y")
	if want := "continuation-token=x%20y&list-type=2&prefix=a%2Fb%2F"; got != want {
		t.Fatalf("canonical query %q, want %q", got, want)
	}
	if got := canonicalURI(""); got != "/" {
		t.Fatalf("empty path %q", got)
	}
	if got := canonicalURI("/a b/c"); got != "/a%20b/c" {
		t.Fatalf("path %q", got)
	}
}

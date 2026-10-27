package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"
)

// putObject uploads one file with the semantics the platform's bucket expects: S3 verifies the SHA-256 the
// job sends, and an object that already exists is refused rather than replaced (the bucket is versioned, so a
// replacement would be recoverable — refusing is the cheaper guarantee). The upload key cannot read back, so
// the checksum S3 checks is the verification.
func putObject(ctx context.Context, job settings, client *http.Client, bucket, key, path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read %s", path)
	}
	sum := sha256.Sum256(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpointOf(job, bucket)+"/"+escapePath(key),
		bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cannot build the request")
	}
	request.Header.Set("If-None-Match", "*")
	request.Header.Set("x-amz-checksum-sha256", base64.StdEncoding.EncodeToString(sum[:]))
	request.Header.Set("x-amz-content-sha256", hex.EncodeToString(sum[:]))
	sign(request, job, hex.EncodeToString(sum[:]), time.Now())
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	}
	response, err := doWithRetry(client, request)
	if err != nil {
		return fmt.Errorf("the upload did not complete (%v)", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return nil
	case response.StatusCode == http.StatusPreconditionFailed:
		return fmt.Errorf("an object with that name already exists")
	case response.StatusCode == http.StatusForbidden, response.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("the upload was refused (403)")
	}
	return fmt.Errorf("the upload was refused (%d)", response.StatusCode)
}

// Attempts and the gap between them: a pod's first seconds can fail to reach a Service before the node's
// rules are programmed, and the AWS CLI this replaces retried by default. A backup is not lost to that.
const transportAttempts = 5

// transportBackoff is the gap between attempts; a test sets it to zero.
var transportBackoff = 5 * time.Second

// doWithRetry sends one request, retrying a transport failure until the attempts are used. A status the
// server answers is not retried: that is an answer, not a transient.
func doWithRetry(client *http.Client, request *http.Request) (*http.Response, error) {
	var failure error
	for attempt := 1; attempt <= transportAttempts; attempt++ {
		response, err := client.Do(request)
		if err == nil {
			return response, nil
		}
		failure = err
		if attempt == transportAttempts {
			break
		}
		time.Sleep(transportBackoff)
		if request.GetBody != nil {
			body, err := request.GetBody()
			if err != nil {
				return nil, fmt.Errorf("%s, and the request could not be replayed", transportReason(failure))
			}
			request.Body = body
		}
	}
	return nil, fmt.Errorf("%s after %d attempts", transportReason(failure), transportAttempts)
}

// transportReason names why a request never completed: a resolved name, a refused connection, a timeout, a
// TLS failure or the proxy. It carries no URL and no content, so a job's log stays safe to read.
func transportReason(err error) string {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns"
	}
	var operation *net.OpError
	if errors.As(err, &operation) {
		if operation.Timeout() {
			return "timeout"
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			return "refused"
		}
		return "dial"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if strings.Contains(err.Error(), "tls:") {
		return "tls"
	}
	return "transport"
}

// endpointOf is the bucket's endpoint: the override when one is set, else AWS's virtual-hosted form.
func endpointOf(job settings, bucket string) string {
	if job.Endpoint != "" {
		return strings.TrimSuffix(job.Endpoint, "/") + "/" + bucket
	}
	return "https://" + bucket + ".s3." + job.Region + ".amazonaws.com"
}

// sign adds the SigV4 headers for one request: the host, every x-amz-* header and If-None-Match, with the
// payload's own SHA-256. It is the same canonicalisation `aws s3api` sends, checked against AWS's published
// example in the repository's v2 tests.
func sign(request *http.Request, job settings, payloadHash string, now time.Time) {
	request.Header.Set("x-amz-date", now.UTC().Format("20060102T150405Z"))
	if job.Token != "" {
		request.Header.Set("x-amz-security-token", job.Token)
	}
	names := []string{"host"}
	for name := range request.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-") || lower == "if-none-match" {
			names = append(names, lower)
		}
	}
	sort.Strings(names)
	headers := ""
	for _, name := range names {
		value := request.Header.Get(name)
		if name == "host" {
			value = request.URL.Host
		}
		headers += name + ":" + strings.TrimSpace(value) + "\n"
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{request.Method, canonicalURI(request.URL.Path), "", headers, signed,
		payloadHash}, "\n")
	date := now.UTC().Format("20060102")
	scope := date + "/" + job.Region + "/s3/aws4_request"
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", request.Header.Get("x-amz-date"), scope,
		hexSHA256([]byte(canonical))}, "\n")
	signature := hmacSHA256(signingKey(job.SecretKey, date, job.Region), toSign)
	request.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", job.AccessKey, scope, signed,
		hex.EncodeToString(signature)))
}

// canonicalURI is the path AWS signs: every segment encoded, and "/" for a root.
func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = awsEscape(segment)
	}
	return strings.Join(segments, "/")
}

// escapePath encodes an object key for the wire, segment by segment, so a slash stays a slash.
func escapePath(key string) string {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// awsEscape is AWS's URI encoding for a canonical request.
func awsEscape(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		character := value[i]
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' || strings.IndexByte("-._~", character) >= 0 {
			out.WriteByte(character)
			continue
		}
		fmt.Fprintf(&out, "%%%02X", character)
	}
	return out.String()
}

func signingKey(secret, date, region string) []byte {
	return hmacSHA256(hmacSHA256(hmacSHA256(hmacSHA256([]byte("AWS4"+secret), date), region), "s3"),
		"aws4_request")
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(value))
	return mac.Sum(nil)
}

func hexSHA256(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

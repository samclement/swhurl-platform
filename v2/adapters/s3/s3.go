// Package s3 lists one bucket prefix: the objects an instance's backups live under, and nothing else. It
// takes the operator's own credentials as arguments — never a file, the environment or a secret — and speaks
// the evidence vocabulary the pure layer defines, so a failed read is never an empty bucket.
package s3

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// The refusal: a request this adapter cannot make, which is not evidence about the bucket.
const RefusedRequest = "refused_request"

// Error names a code, the subject it is about and a fixed reason. It never carries a credential.
type Error struct{ Code, Subject, Reason string }

func (e *Error) Error() string { return fmt.Sprintf("%s: %s: %s", e.Code, e.Subject, e.Reason) }

func refuse(subject, reason string) error { return &Error{RefusedRequest, subject, reason} }

// Reader lists one bucket prefix and reads one object under it. A listing the server answers with —
// including a refusal — is evidence; a request it cannot make is an error. Get reports absence as the
// "not_found" code, so a caller can tell an incomplete upload from a bucket it could not reach.
type Reader interface {
	List(ctx context.Context, query observe.ArchiveQuery) (observe.ArchiveList, error)
	Get(ctx context.Context, query observe.ArchiveQuery, key string) ([]byte, error)
	// PresignURL is a time-limited read of one object, for a caller that must hand the read to someone
	// else — a restore pod — without giving it the bucket credential.
	PresignURL(ctx context.Context, query observe.ArchiveQuery, key string, ttl time.Duration) (string, error)
}

const (
	// maxBody bounds one listing page; a prefix with more is read page by page.
	maxBody = 1 << 20
	// maxPages bounds how many pages one listing follows, so a wrong prefix cannot walk a bucket.
	maxPages = 10
	// MaxPresignTTL is SigV4's own ceiling for a query-string signature: the longest a caller may hand a
	// read of one object to someone else.
	MaxPresignTTL = 7 * 24 * time.Hour
)

// ParseURL reads the s3:// URI the platform profile states: a bucket and the prefix under it.
func ParseURL(uri string) (observe.ArchiveQuery, error) {
	rest, found := strings.CutPrefix(uri, "s3://")
	if !found {
		return observe.ArchiveQuery{}, refuse("uri", "expected_an_s3_uri")
	}
	bucket, prefix, _ := strings.Cut(rest, "/")
	if bucket == "" {
		return observe.ArchiveQuery{}, refuse("uri", "expected_a_bucket")
	}
	return observe.ArchiveQuery{Bucket: bucket, Prefix: prefix}, nil
}

// Client lists one bucket prefix in one region, over the TLS client it is given.
type Client struct {
	region      string
	credentials Credentials
	http        *http.Client
}

// New returns a reader for one region. An empty region is discovered from the bucket itself, so the platform
// profile keeps no region field.
func New(region string, credentials Credentials, client *http.Client) Reader {
	return Client{region: region, credentials: credentials, http: client}
}

// DiscoverRegion asks the bucket which region it is in, without credentials: S3 answers a request to the
// wrong endpoint with the header naming the right one, and a bucket in us-east-1 answers from the global
// endpoint.
func DiscoverRegion(ctx context.Context, bucket string, client *http.Client) (string, error) {
	if bucket == "" {
		return "", refuse("bucket", "required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://"+bucket+".s3.amazonaws.com/", nil)
	if err != nil {
		return "", refuse("bucket", "invalid_name")
	}
	response, err := client.Do(request)
	if err != nil {
		return "", &Error{"status", "bucket", reasonOf(err)}
	}
	defer response.Body.Close()
	if region := response.Header.Get("x-amz-bucket-region"); region != "" {
		return region, nil
	}
	if response.StatusCode == http.StatusOK {
		return "us-east-1", nil
	}
	return "", &Error{"status", "bucket", observe.ReasonStatus}
}

// List follows the prefix's listing, page by page, and returns every object under it. A page the server
// refuses, or an answer this adapter cannot read, is evidence with a fixed reason.
func (c Client) List(ctx context.Context, query observe.ArchiveQuery) (observe.ArchiveList, error) {
	if query.Bucket == "" {
		return observe.ArchiveList{}, refuse("bucket", "required")
	}
	if c.region == "" {
		region, err := DiscoverRegion(ctx, query.Bucket, c.http)
		if err != nil {
			// A bucket whose region cannot be named cannot be listed: a failed read, never an empty prefix.
			return unavailable(query, failureReason(err)), nil
		}
		c.region = region
	}
	listed := observe.ArchiveList{Query: query, Status: observe.Present, Objects: []observe.Archive{}}
	token := ""
	for page := 0; page < maxPages; page++ {
		objects, next, failed, err := c.page(ctx, query, token)
		if err != nil {
			return observe.ArchiveList{}, err
		}
		if failed != nil {
			return *failed, nil
		}
		listed.Objects = append(listed.Objects, objects...)
		if next == "" {
			return listed, nil
		}
		token = next
	}
	return unavailable(query, observe.ReasonStatus), nil
}

// page reads one listing page.
func (c Client) page(ctx context.Context, query observe.ArchiveQuery, token string) ([]observe.Archive, string,
	*observe.ArchiveList, error) {
	request, err := c.request(ctx, query, token)
	if err != nil {
		return nil, "", nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		failed := unavailable(query, reasonOf(err))
		return nil, "", &failed, nil
	}
	defer response.Body.Close()
	if failed, ok := statusFailure(query, response.StatusCode); ok {
		return nil, "", &failed, nil
	}
	body, failed := readBody(query, response.Body)
	if failed != nil {
		return nil, "", failed, nil
	}
	return parseListing(query, body)
}

// statusFailure maps a server's status to the reason the report carries. Only a listing that succeeded says
// anything about the objects, so every other status is a failed read.
func statusFailure(query observe.ArchiveQuery, status int) (observe.ArchiveList, bool) {
	switch status {
	case http.StatusOK:
		return observe.ArchiveList{}, false
	case http.StatusForbidden, http.StatusUnauthorized:
		return unavailable(query, observe.ReasonForbidden), true
	case http.StatusNotFound:
		return unavailable(query, observe.ReasonNotFound), true
	}
	return unavailable(query, observe.ReasonStatus), true
}

// readBody bounds one page: a listing bigger than the bound is a response this adapter will not read.
func readBody(query observe.ArchiveQuery, reader io.Reader) ([]byte, *observe.ArchiveList) {
	body, err := io.ReadAll(io.LimitReader(reader, maxBody+1))
	if err != nil || len(body) > maxBody {
		failed := unavailable(query, observe.ReasonTooLarge)
		return nil, &failed
	}
	return body, nil
}

// parseListing reads one page's S3 answer: its objects, and the token for the next page when it was cut.
func parseListing(query observe.ArchiveQuery, body []byte) ([]observe.Archive, string, *observe.ArchiveList, error) {
	var listing struct {
		IsTruncated           bool   `xml:"IsTruncated"`
		NextContinuationToken string `xml:"NextContinuationToken"`
		Contents              []struct {
			Key          string `xml:"Key"`
			LastModified string `xml:"LastModified"`
		} `xml:"Contents"`
	}
	if err := xml.Unmarshal(body, &listing); err != nil {
		failed := unavailable(query, observe.ReasonMalformed)
		return nil, "", &failed, nil
	}
	objects := make([]observe.Archive, 0, len(listing.Contents))
	for _, item := range listing.Contents {
		modified, err := time.Parse(time.RFC3339, item.LastModified)
		if err != nil {
			failed := unavailable(query, observe.ReasonMalformed)
			return nil, "", &failed, nil
		}
		objects = append(objects, observe.Archive{Key: item.Key, LastModified: modified})
	}
	if listing.IsTruncated {
		return objects, listing.NextContinuationToken, nil, nil
	}
	return objects, "", nil, nil
}

// request is one signed ListObjectsV2 request for the prefix, or the next page of it.
func (c Client) request(ctx context.Context, query observe.ArchiveQuery, token string) (*http.Request, error) {
	values := url.Values{"list-type": {"2"}, "prefix": {query.Prefix}}
	if token != "" {
		values.Set("continuation-token", token)
	}
	endpoint := "https://" + query.Bucket + ".s3." + c.region + ".amazonaws.com/?" + values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, refuse("endpoint", "invalid")
	}
	sign(request, c.region, c.credentials, time.Now())
	return request, nil
}

// Get reads one object under the prefix, bounded like a listing page. A 404 is the "not_found" code; every
// other failure is the reason the report would carry.
func (c Client) Get(ctx context.Context, query observe.ArchiveQuery, key string) ([]byte, error) {
	if query.Bucket == "" || key == "" {
		return nil, refuse("key", "required")
	}
	if !strings.HasPrefix(key, query.Prefix) {
		return nil, refuse("key", "not_under_the_prefix")
	}
	request, err := c.objectRequest(ctx, query.Bucket, key)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, &Error{"status", "object", reasonOf(err)}
	}
	defer response.Body.Close()
	if failure := objectFailure(response.StatusCode); failure != nil {
		return nil, failure
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return nil, &Error{"status", "object", observe.ReasonTooLarge}
	}
	return body, nil
}

// objectRequest is one signed GET for an object under a bucket, discovering the region when it is not known.
func (c Client) objectRequest(ctx context.Context, bucket, key string) (*http.Request, error) {
	if c.region == "" {
		region, err := DiscoverRegion(ctx, bucket, c.http)
		if err != nil {
			return nil, err
		}
		c.region = region
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://"+bucket+".s3."+c.region+".amazonaws.com/"+awsEscapePath(key), nil)
	if err != nil {
		return nil, refuse("key", "invalid")
	}
	sign(request, c.region, c.credentials, time.Now())
	return request, nil
}

// PresignURL signs one GET in the query string, so the holder can read that one object until the URL
// expires. It is the same credentials and the same canonicalisation as a read, confined to the instance's own
// prefix like Get, and the expiry is bounded by SigV4's own maximum.
func (c Client) PresignURL(ctx context.Context, query observe.ArchiveQuery, key string, ttl time.Duration) (string, error) {
	if query.Bucket == "" || key == "" {
		return "", refuse("key", "required")
	}
	if !strings.HasPrefix(key, query.Prefix) {
		return "", refuse("key", "not_under_the_prefix")
	}
	if ttl <= 0 || ttl > MaxPresignTTL {
		return "", refuse("ttl", "expected_a_duration_up_to_seven_days")
	}
	region := c.region
	if region == "" {
		discovered, err := DiscoverRegion(ctx, query.Bucket, c.http)
		if err != nil {
			return "", err
		}
		region = discovered
	}
	host := query.Bucket + ".s3." + region + ".amazonaws.com"
	return presignedURL(host, "/"+awsEscapePath(key), region, c.credentials, ttl, time.Now()), nil
}

// objectFailure is the code and reason one object read's status means: a 404 is the only absence, and every
// other non-200 is a failed read.
func objectFailure(status int) *Error {
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return &Error{"not_found", "object", observe.ReasonNotFound}
	case http.StatusForbidden, http.StatusUnauthorized:
		return &Error{"status", "object", observe.ReasonForbidden}
	}
	return &Error{"status", "object", observe.ReasonStatus}
}

// awsEscapePath encodes an object key's path segments the way the canonical request does.
func awsEscapePath(key string) string {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = awsEscape(segment)
	}
	return strings.Join(segments, "/")
}

func unavailable(query observe.ArchiveQuery, reason string) observe.ArchiveList {
	return observe.ArchiveList{Query: query, Status: observe.Unavailable, Reason: reason}
}

// failureReason is the reason an adapter error states, for the evidence a failed read leaves.
func failureReason(err error) string {
	var failure *Error
	if errors.As(err, &failure) && failure.Reason != "" {
		return failure.Reason
	}
	return observe.ReasonStatus
}

// reasonOf maps a transport failure to the fixed reason a report carries. A deadline is timed out whichever
// layer reports it: the context, the client's own timeout, or the connection.
func reasonOf(err error) string {
	var timeout interface{ Timeout() bool }
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) ||
		errors.As(err, &timeout) && timeout.Timeout() {
		return observe.ReasonTimedOut
	}
	return observe.ReasonTransport
}

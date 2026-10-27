package s3

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Credentials are the operator's own: the caller supplies them, and this adapter reads no file and no
// environment variable.
type Credentials struct{ AccessKey, SecretKey, SessionToken string }

// sign adds the SigV4 headers for one request, at the supplied time. It signs the Host header, every
// x-amz-* header (so a session token travels signed, as S3 requires) and Range when it is present. The
// payload is unsigned over TLS, which is what S3 accepts for a GET.
func sign(request *http.Request, region string, credentials Credentials, now time.Time) {
	request.Header.Set("x-amz-date", now.UTC().Format("20060102T150405Z"))
	if request.Header.Get("x-amz-content-sha256") == "" {
		request.Header.Set("x-amz-content-sha256", "UNSIGNED-PAYLOAD")
	}
	if credentials.SessionToken != "" {
		request.Header.Set("x-amz-security-token", credentials.SessionToken)
	}
	signed, canonicalHeaders, canonicalQuery := canonical(request)
	canonicalRequest := strings.Join([]string{
		request.Method, canonicalURI(request.URL.Path), canonicalQuery, canonicalHeaders, signed,
		request.Header.Get("x-amz-content-sha256"),
	}, "\n")
	date := now.UTC().Format("20060102")
	scope := strings.Join([]string{date, region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", request.Header.Get("x-amz-date"), scope,
		hexSHA256([]byte(canonicalRequest))}, "\n")
	key := signingKey(credentials.SecretKey, date, region)
	request.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		credentials.AccessKey, scope, signed, hex.EncodeToString(hmacSHA256(key, stringToSign))))
}

// presignedURL is SigV4's query-string form for one GET: the same canonical request and signing key as a
// signed header, with the scope, the expiry and the signature in the query instead. It is a credential for
// exactly one object until it expires, so a caller hands it to the pod that needs that object and no other.
func presignedURL(host, path, region string, credentials Credentials, ttl time.Duration, now time.Time) string {
	date := now.UTC().Format("20060102")
	stamp := now.UTC().Format("20060102T150405Z")
	scope := strings.Join([]string{date, region, "s3", "aws4_request"}, "/")
	values := url.Values{
		"X-Amz-Algorithm":     {"AWS4-HMAC-SHA256"},
		"X-Amz-Credential":    {credentials.AccessKey + "/" + scope},
		"X-Amz-Date":          {stamp},
		"X-Amz-Expires":       {strconv.FormatInt(int64(ttl/time.Second), 10)},
		"X-Amz-SignedHeaders": {"host"},
	}
	if credentials.SessionToken != "" {
		values.Set("X-Amz-Security-Token", credentials.SessionToken)
	}
	query := canonicalQuery(values.Encode())
	canonicalRequest := strings.Join([]string{
		http.MethodGet, path, query, "host:" + host + "\n", "host", "UNSIGNED-PAYLOAD",
	}, "\n")
	stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", stamp, scope,
		hexSHA256([]byte(canonicalRequest))}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(credentials.SecretKey, date, region), stringToSign))
	return "https://" + host + path + "?" + query + "&X-Amz-Signature=" + signature
}

// canonical is the request's signed header list, its canonical headers and its canonical query.
func canonical(request *http.Request) (signed, headers, query string) {
	names := []string{"host"}
	for name := range request.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-") || lower == "range" {
			names = append(names, lower)
		}
	}
	sort.Strings(names)
	names = dedupe(names)
	for _, name := range names {
		value := request.Host
		if name != "host" {
			value = strings.TrimSpace(request.Header.Get(name))
		}
		if name == "host" && value == "" {
			value = request.URL.Host
		}
		headers += name + ":" + value + "\n"
	}
	return strings.Join(names, ";"), headers, canonicalQuery(request.URL.RawQuery)
}

// canonicalQuery is the query AWS signs: every parameter, percent-encoded, sorted by name, joined with '&'.
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	type pair struct{ name, value string }
	pairs := []pair{}
	for _, part := range strings.Split(raw, "&") {
		name, value, _ := strings.Cut(part, "=")
		pairs = append(pairs, pair{awsEscape(name), awsEscape(unescape(value))})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].name != pairs[j].name {
			return pairs[i].name < pairs[j].name
		}
		return pairs[i].value < pairs[j].value
	})
	terms := make([]string, 0, len(pairs))
	for _, entry := range pairs {
		terms = append(terms, entry.name+"="+entry.value)
	}
	return strings.Join(terms, "&")
}

// canonicalURI is the path AWS signs: every segment percent-encoded, and "/" for the bucket root.
func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = awsEscape(unescape(segment))
	}
	return strings.Join(segments, "/")
}

// awsEscape is AWS's URI encoding for a canonical request: unreserved characters stay, everything else is
// percent-encoded in upper case, and a space is %20.
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

// unescape decodes a percent-encoded component once, so re-encoding is canonical rather than doubled.
func unescape(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '%' && i+2 < len(value) {
			var decoded int
			if _, err := fmt.Sscanf(value[i+1:i+3], "%02X", &decoded); err == nil {
				out.WriteByte(byte(decoded))
				i += 2
				continue
			}
		}
		out.WriteByte(value[i])
	}
	return out.String()
}

func signingKey(secret, date, region string) []byte {
	dated := hmacSHA256([]byte("AWS4"+secret), date)
	scoped := hmacSHA256(dated, region)
	service := hmacSHA256(scoped, "s3")
	return hmacSHA256(service, "aws4_request")
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

func dedupe(names []string) []string {
	out := names[:0]
	for i, name := range names {
		if i == 0 || name != names[i-1] {
			out = append(out, name)
		}
	}
	return out
}

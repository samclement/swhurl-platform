package s3

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// Credentials are the operator's own: the caller supplies them, and this adapter reads no file and no
// environment variable.
type Credentials struct{ AccessKey, SecretKey, SessionToken string }

// unsignedPayload is what S3 accepts in place of a body's hash over TLS, which is how every read here is signed.
const unsignedPayload = "UNSIGNED-PAYLOAD"

// signer is AWS's own SigV4 signer as S3 needs it: the path is signed as the request carries it, already
// escaped once, never escaped again.
var signer = v4.NewSigner(func(options *v4.SignerOptions) { options.DisableURIPathEscaping = true })

// sign adds the SigV4 headers for one request, at the supplied time. The Host header, every x-amz-* header (so a
// session token travels signed, as S3 requires) and Range when it is present are signed. The payload is unsigned
// over TLS unless the caller stated its hash.
func sign(request *http.Request, region string, credentials Credentials, now time.Time) {
	payload := request.Header.Get("x-amz-content-sha256")
	if payload == "" {
		payload = unsignedPayload
		request.Header.Set("x-amz-content-sha256", payload)
	}
	// SignHTTP fails only on a request it cannot read, which the caller built; the request then goes unsigned and
	// S3 refuses it, which is the failure the caller reports.
	_ = signer.SignHTTP(context.Background(), awsCredentials(credentials), request, payload, "s3", region, now)
}

// presignedURL is SigV4's query-string form for one GET: the scope, the expiry and the signature in the query. It
// is a credential for exactly one object until it expires, so a caller hands it to the pod that needs that object
// and no other.
func presignedURL(host, path, region string, credentials Credentials, ttl time.Duration, now time.Time) string {
	request, err := http.NewRequest(http.MethodGet, "https://"+host+path, nil)
	if err != nil {
		return ""
	}
	query := request.URL.Query()
	query.Set("X-Amz-Expires", strconv.FormatInt(int64(ttl/time.Second), 10))
	request.URL.RawQuery = query.Encode()
	signed, _, err := signer.PresignHTTP(context.Background(), awsCredentials(credentials), request,
		unsignedPayload, "s3", region, now)
	if err != nil {
		return ""
	}
	return signed
}

// awsCredentials is the operator's credentials in the SDK's type.
func awsCredentials(credentials Credentials) aws.Credentials {
	return aws.Credentials{AccessKeyID: credentials.AccessKey, SecretAccessKey: credentials.SecretKey,
		SessionToken: credentials.SessionToken}
}

package kube

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// Reviewer asks the API server whose token a caller presented, for one audience. It is a port of its own, beside
// Reader and Writer, so only a use case that authenticates callers can reach it.
type Reviewer interface {
	Review(ctx context.Context, token, audience string) (TokenReview, error)
}

// TokenReview is the API server's answer about one token: whether it vouches for it, for which audiences, and the
// user it belongs to. A token it does not vouch for is an answer, not an error.
type TokenReview struct {
	Authenticated bool
	Audiences     []string
	Username      string
}

var _ Reviewer = (*API)(nil)

// Review creates a TokenReview for the token and audience and reads the status the server answers. The token
// travels in the request body only; neither it nor the answer, which repeats it, is ever part of an error.
func (a *API) Review(ctx context.Context, token, audience string) (TokenReview, error) {
	if token == "" || audience == "" {
		return TokenReview{}, &Error{RefusedRequest, "tokenreview", "token_and_audience_required"}
	}
	request, err := json.Marshal(map[string]any{
		"apiVersion": "authentication.k8s.io/v1", "kind": "TokenReview",
		"spec": map[string]any{"token": token, "audiences": []string{audience}},
	})
	if err != nil {
		return TokenReview{}, &Error{RefusedRequest, "tokenreview", "unencodable"}
	}
	body, failure := a.exchange(ctx, http.MethodPost, "/apis/authentication.k8s.io/v1/tokenreviews", nil, request,
		"application/json")
	if failure != nil {
		return TokenReview{}, &Error{Failed, "tokenreview", failure.Reason}
	}
	var answer struct {
		Status *struct {
			Authenticated bool     `json:"authenticated"`
			Audiences     []string `json:"audiences"`
			User          struct {
				Username string `json:"username"`
			} `json:"user"`
		} `json:"status"`
	}
	if json.Unmarshal(body, &answer) != nil || answer.Status == nil {
		return TokenReview{}, &Error{Failed, "tokenreview", observe.ReasonMalformed}
	}
	return TokenReview{answer.Status.Authenticated, answer.Status.Audiences, answer.Status.User.Username}, nil
}

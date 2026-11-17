// Package sacaller tells catalyst-api which in-cluster workload sent a
// request. The workload presents its projected ServiceAccount token as
// `Authorization: Bearer`; a TokenReview of the cluster the API runs in
// judges the token and names the account. The token is never read here:
// the review checks its signature, its expiry, its audience and that the
// ServiceAccount still exists.
//
// The internal routes of internal/handler (internal_auth.go) and the
// agent-token route of internal/agenttoken call the same function.
package sacaller

import (
	"context"
	"fmt"
	"strings"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Validate runs a TokenReview against the cluster's authentication chain.
// Returns the resolved username on success, for example
// system:serviceaccount:<namespace>:<name>, or an error describing why the
// token was rejected.
//
// Production wires core to a real kubernetes.Interface; tests inject a
// fake.NewSimpleClientset and prepend a reactor for the `tokenreviews`
// resource so the apiserver round-trip is mocked.
func Validate(ctx context.Context, core kubernetes.Interface, bearer string) (string, error) {
	if bearer == "" {
		return "", fmt.Errorf("empty bearer token")
	}
	if core == nil {
		return "", fmt.Errorf("no cluster client to review the token with")
	}
	tr := &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: bearer}}
	resp, err := core.AuthenticationV1().TokenReviews().Create(ctx, tr, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("token review API call failed: %w", err)
	}
	if !resp.Status.Authenticated {
		// resp.Status.Error is operator-actionable (e.g. "token expired").
		// Returned verbatim so the caller can log it.
		detail := resp.Status.Error
		if detail == "" {
			detail = "token not authenticated by apiserver"
		}
		return "", fmt.Errorf("token review rejected: %s", detail)
	}
	user := strings.TrimSpace(resp.Status.User.Username)
	if user == "" {
		return "", fmt.Errorf("token review returned empty username")
	}
	return user, nil
}

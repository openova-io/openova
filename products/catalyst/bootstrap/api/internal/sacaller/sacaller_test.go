package sacaller

import (
	"context"
	"errors"
	"strings"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

// review returns a fake cluster whose TokenReview answers with status, or
// fails with err when err is not nil. seen receives the token reviewed.
func review(status authnv1.TokenReviewStatus, err error, seen *string) *fakek8s.Clientset {
	client := fakek8s.NewSimpleClientset()
	client.PrependReactor("create", "tokenreviews", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		tr := action.(clienttesting.CreateAction).GetObject().(*authnv1.TokenReview)
		*seen = tr.Spec.Token
		if err != nil {
			return true, nil, err
		}
		out := tr.DeepCopy()
		out.Status = status
		return true, out, nil
	})
	return client
}

// TestValidateReturnsTheReviewedUser: the token goes to the review
// unchanged and the user name the review resolved comes back.
func TestValidateReturnsTheReviewedUser(t *testing.T) {
	var seen string
	core := review(authnv1.TokenReviewStatus{Authenticated: true,
		User: authnv1.UserInfo{Username: " system:serviceaccount:ops:ops-records "}}, nil, &seen)
	user, err := Validate(context.Background(), core, "projected-token")
	if err != nil || user != "system:serviceaccount:ops:ops-records" {
		t.Fatalf("Validate = %q, %v", user, err)
	}
	if seen != "projected-token" {
		t.Fatalf("the review saw the token %q", seen)
	}
}

// TestValidateRefuses: every way a token is not accepted is an error, and
// no error ever names the token.
func TestValidateRefuses(t *testing.T) {
	var seen string
	cases := []struct {
		name   string
		core   *fakek8s.Clientset
		bearer string
		want   string
	}{
		{"no bearer", review(authnv1.TokenReviewStatus{Authenticated: true}, nil, &seen), "", "empty bearer token"},
		{"the review fails", review(authnv1.TokenReviewStatus{}, errors.New("connection refused"), &seen), "secret-token", "token review API call failed: connection refused"},
		{"not authenticated", review(authnv1.TokenReviewStatus{Error: "token expired"}, nil, &seen), "secret-token", "token review rejected: token expired"},
		{"not authenticated, no reason", review(authnv1.TokenReviewStatus{}, nil, &seen), "secret-token", "token review rejected: token not authenticated by apiserver"},
		{"no user name", review(authnv1.TokenReviewStatus{Authenticated: true}, nil, &seen), "secret-token", "token review returned empty username"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			user, err := Validate(context.Background(), c.core, c.bearer)
			if err == nil || user != "" {
				t.Fatalf("Validate = %q, %v; want an error", user, err)
			}
			if !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("error %q, want %q and never the token", err, c.want)
			}
		})
	}
	if _, err := Validate(context.Background(), nil, "secret-token"); err == nil || err.Error() != "no cluster client to review the token with" {
		t.Fatalf("no cluster client: %v", err)
	}
}

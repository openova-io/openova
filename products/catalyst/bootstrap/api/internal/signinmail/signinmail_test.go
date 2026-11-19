package signinmail

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// withClient makes Resolve read from cli (or fail to get a client when err is set).
func withClient(t *testing.T, cli kubernetes.Interface, err error) {
	t.Helper()
	prev := ClientFor
	ClientFor = func() (kubernetes.Interface, error) { return cli, err }
	t.Cleanup(func() { ClientFor = prev })
}

// withEnv sets the environment a catalyst-api rendered before the mail server's
// Secret existed: the chart's default host and no credential.
func withEnv(t *testing.T, host, user, pass string) {
	t.Helper()
	t.Setenv("CATALYST_SMTP_HOST", host)
	t.Setenv("CATALYST_SMTP_PORT", "")
	t.Setenv("CATALYST_SMTP_FROM", "")
	t.Setenv("CATALYST_SMTP_USER", user)
	t.Setenv("CATALYST_SMTP_PASS", pass)
}

func mailSecret(data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: ObjectName}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

var written = map[string]string{
	"smtp-host": "mail.t1.example.test", "smtp-port": "587", "smtp-from": "noreply@t1.example.test",
	"smtp-user": "noreply", "smtp-pass": "generated-in-cluster",
}

// TestResolve_SecretWrittenAfterStart: the mail server's setup Job wrote the
// Secret after the process started with no credential in its environment. The
// next sign-in code goes to the Sovereign's own server with its credential.
func TestResolve_SecretWrittenAfterStart(t *testing.T) {
	withEnv(t, "", "", "")
	withClient(t, fake.NewSimpleClientset(mailSecret(written)), nil)
	got := Resolve(context.Background())
	want := Settings{Host: "mail.t1.example.test", Port: "587", From: "noreply@t1.example.test", User: "noreply", Pass: "generated-in-cluster"}
	if got != want {
		t.Fatalf("Resolve = %+v, want %+v", got, want)
	}
	if got.Addr() != "mail.t1.example.test:587" {
		t.Fatalf("Addr = %q", got.Addr())
	}
}

// TestResolve_SecretWinsOverEnvironment: an environment rendered from an older
// Secret does not override the one the mail server holds now.
func TestResolve_SecretWinsOverEnvironment(t *testing.T) {
	withEnv(t, "old.example.test", "old-user", "old-pass")
	withClient(t, fake.NewSimpleClientset(mailSecret(written)), nil)
	if got := Resolve(context.Background()); got.Host != "mail.t1.example.test" || got.User != "noreply" || got.Pass != "generated-in-cluster" {
		t.Fatalf("Resolve = %+v, want the Secret's host and credential", got)
	}
}

// TestResolve_EnvironmentWhenNoSecret: no Secret, no in-cluster identity, or a
// Secret without host, user or password: the environment answers, with the
// defaults for host, port and sender, and the two credentials are never mixed.
func TestResolve_EnvironmentWhenNoSecret(t *testing.T) {
	withEnv(t, "relay.example.test", "env-user", "env-pass")
	envWant := Settings{Host: "relay.example.test", Port: "587", From: "noreply@openova.io", User: "env-user", Pass: "env-pass"}

	withClient(t, fake.NewSimpleClientset(), nil)
	if got := Resolve(context.Background()); got != envWant {
		t.Fatalf("no Secret: Resolve = %+v, want %+v", got, envWant)
	}
	withClient(t, nil, errors.New("not in a cluster"))
	if got := Resolve(context.Background()); got != envWant {
		t.Fatalf("no client: Resolve = %+v, want %+v", got, envWant)
	}
	for _, drop := range []string{"smtp-host", "smtp-user", "smtp-pass"} {
		partial := map[string]string{}
		for k, v := range written {
			if k != drop {
				partial[k] = v
			}
		}
		withClient(t, fake.NewSimpleClientset(mailSecret(partial)), nil)
		if got := Resolve(context.Background()); got != envWant {
			t.Fatalf("Secret without %s: Resolve = %+v, want %+v", drop, got, envWant)
		}
	}
}

// TestFromEnv_Defaults: an empty environment gives the catalyst-api's defaults.
func TestFromEnv_Defaults(t *testing.T) {
	withEnv(t, "", "", "")
	want := Settings{Host: "stalwart-web.stalwart.svc.cluster.local", Port: "587", From: "noreply@openova.io"}
	if got := FromEnv(); got != want {
		t.Fatalf("FromEnv = %+v, want %+v", got, want)
	}
}

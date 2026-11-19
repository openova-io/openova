package signinmail

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/core/controllers/pkg/upstream"
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

// variablesDocument is the ConfigMap flux-system/sovereign-variables with the
// given keys.
func variablesDocument(kv ...string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: upstream.Namespace, Name: upstream.Name}, Data: map[string]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		cm.Data[kv[i]] = kv[i+1]
	}
	return cm
}

func relaySecret(user, pass string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: "sovereign-mail-relay"},
		Data:       map[string][]byte{"smtp-user": []byte(user), "smtp-pass": []byte(pass)},
	}
}

var relayVariables = []string{
	"MAIL_MODE", "relay", "MAIL_RELAY_HOST", "smtp.relay.example.net", "MAIL_RELAY_PORT", "2525",
	"MAIL_FROM", "noreply@t2.example.test", "MAIL_RELAY_SECRET", "sovereign-mail-relay",
}

// TestResolve_RelayMode (SOV-323): the variables document says MAIL_MODE
// relay. The code goes to the relay, as MAIL_FROM, with the credential the
// relay issued for this Sovereign, even when the local Secret and the
// environment hold another one.
func TestResolve_RelayMode(t *testing.T) {
	withEnv(t, "env.example.test", "env-user", "env-pass")
	withClient(t, fake.NewSimpleClientset(variablesDocument(relayVariables...), mailSecret(written), relaySecret("t2", "issued-for-t2")), nil)
	want := Settings{Host: "smtp.relay.example.net", Port: "2525", From: "noreply@t2.example.test", User: "t2", Pass: "issued-for-t2"}
	if got := Resolve(context.Background()); got != want {
		t.Fatalf("Resolve = %+v, want %+v", got, want)
	}
}

// TestResolve_RelayWithoutCredential (SOV-323): in relay mode a relay Secret
// that is absent, unnamed or incomplete gives the relay's address with no
// credential. The local Secret and the environment are never used instead.
func TestResolve_RelayWithoutCredential(t *testing.T) {
	withEnv(t, "env.example.test", "env-user", "env-pass")
	want := Settings{Host: "smtp.relay.example.net", Port: "2525", From: "noreply@t2.example.test"}
	unnamed := append(append([]string{}, relayVariables[:8]...), "MAIL_RELAY_SECRET", "")
	for name, objects := range map[string][]runtime.Object{
		"no relay Secret": {variablesDocument(relayVariables...), mailSecret(written)},
		"no Secret named": {variablesDocument(unnamed...), mailSecret(written), relaySecret("t2", "issued-for-t2")},
		"no smtp-pass":    {variablesDocument(relayVariables...), mailSecret(written), relaySecret("t2", "")},
		"no smtp-user":    {variablesDocument(relayVariables...), mailSecret(written), relaySecret("", "issued-for-t2")},
	} {
		withClient(t, fake.NewSimpleClientset(objects...), nil)
		if got := Resolve(context.Background()); got != want {
			t.Errorf("%s: Resolve = %+v, want %+v", name, got, want)
		}
	}
	noPort := append(append([]string{}, relayVariables...), "MAIL_RELAY_PORT", "")
	withClient(t, fake.NewSimpleClientset(variablesDocument(noPort...), relaySecret("t2", "issued-for-t2")), nil)
	if got := Resolve(context.Background()); got.Port != "587" || got.User != "t2" {
		t.Errorf("no MAIL_RELAY_PORT: Resolve = %+v, want port 587 and the relay credential", got)
	}
}

// TestResolve_LocalModeIgnoresRelay (SOV-323): MAIL_MODE local, or a document
// that holds no MAIL_MODE, keeps the local path: the relay Secret is not read.
func TestResolve_LocalModeIgnoresRelay(t *testing.T) {
	withEnv(t, "", "", "")
	local := Settings{Host: "mail.t1.example.test", Port: "587", From: "noreply@t1.example.test", User: "noreply", Pass: "generated-in-cluster"}
	localKeys := append(append([]string{}, relayVariables...), "MAIL_MODE", "local")
	for name, doc := range map[string]*corev1.ConfigMap{
		"MAIL_MODE local": variablesDocument(localKeys...),
		"no MAIL_MODE":    variablesDocument("RELEASE_CHANNEL", "stable"),
	} {
		withClient(t, fake.NewSimpleClientset(doc, mailSecret(written), relaySecret("t2", "issued-for-t2")), nil)
		if got := Resolve(context.Background()); got != local {
			t.Errorf("%s: Resolve = %+v, want %+v", name, got, local)
		}
	}
}

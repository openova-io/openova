// Package signinmail says where the catalyst-api submits a sign-in code:
// host, port, sender address and the credential of the submission account.
//
// On a Sovereign the setup Job of its own mail server (bootstrap-kit slot
// 95, chart platform/stalwart-sovereign) writes these settings into the
// Secret catalyst-system/sovereign-smtp-credentials once the server knows
// the credential. The process environment (CATALYST_SMTP_*) is filled from
// that Secret only when the catalyst chart renders, and a container's
// environment is fixed at its start. On a new Sovereign the chart renders
// before the mail server has written the Secret, so the environment alone
// would hold no credential until some later upgrade and restart. Resolve
// therefore reads the Secret on every send and falls back to the
// environment, the same way as the live read of the Anthropic credential
// (#6163).
//
// A Sovereign whose variables document (ConfigMap
// flux-system/sovereign-variables, contract C5) says MAIL_MODE relay runs no
// mail server of its own. Its codes go to MAIL_RELAY_HOST:MAIL_RELAY_PORT as
// MAIL_FROM, with the keys smtp-user and smtp-pass of the Secret
// catalyst-system/<MAIL_RELAY_SECRET>: a credential the relay issued for
// this Sovereign alone. In relay mode neither the Secret of the local mail
// server nor the environment is read; a relay Secret that is absent or
// incomplete gives the relay's address with no credential, so the relay
// refuses the code and no other server ever receives it.
package signinmail

import (
	"context"
	"os"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/core/controllers/pkg/upstream"
)

const (
	// Namespace and ObjectName name the Secret the mail server's setup Job
	// writes, and which the catalyst chart reads with Helm lookup.
	Namespace  = "catalyst-system"
	ObjectName = "sovereign-smtp-credentials"

	readBudget = 5 * time.Second

	defaultHost = "stalwart-web.stalwart.svc.cluster.local"
	defaultPort = "587"
	defaultFrom = "noreply@openova.io"
)

// Settings is where and as whom a sign-in code is submitted.
type Settings struct {
	Host, Port, From, User, Pass string
}

// Addr is host:port for net/smtp.
func (s Settings) Addr() string { return s.Host + ":" + s.Port }

// ClientFor builds the client that reads the Secret. A test replaces it; an
// error or a nil client means no in-cluster identity, and Resolve answers
// from the environment.
var ClientFor = func() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// FromEnv returns the settings of the process environment, with the
// defaults of the catalyst-api for host, port and sender.
func FromEnv() Settings {
	s := Settings{
		Host: os.Getenv("CATALYST_SMTP_HOST"),
		Port: os.Getenv("CATALYST_SMTP_PORT"),
		From: os.Getenv("CATALYST_SMTP_FROM"),
		User: os.Getenv("CATALYST_SMTP_USER"),
		Pass: os.Getenv("CATALYST_SMTP_PASS"),
	}
	if s.Host == "" {
		s.Host = defaultHost
	}
	if s.Port == "" {
		s.Port = defaultPort
	}
	if s.From == "" {
		s.From = defaultFrom
	}
	return s
}

// Resolve returns the settings of the Secret when it holds a host, a user
// and a password, and the settings of the environment otherwise: when the
// Secret is absent, unreadable or incomplete, or when the process has no
// in-cluster identity. It never mixes the two credentials. When the variables
// document says MAIL_MODE relay, the relay answers instead (relayMode).
func Resolve(ctx context.Context) Settings {
	env := FromEnv()
	cli, err := ClientFor()
	if err != nil || cli == nil {
		return env
	}
	ctx, cancel := context.WithTimeout(ctx, readBudget)
	defer cancel()
	if relay, ok := relayMode(ctx, cli); ok {
		return relay
	}
	sec, err := cli.CoreV1().Secrets(Namespace).Get(ctx, ObjectName, metav1.GetOptions{})
	if err != nil || sec == nil {
		return env
	}
	return fromSecret(sec.Data, env)
}

// relayMode reads the variables document. ok is false when the document
// cannot be read or its MAIL_MODE is not relay; the local path answers then.
// In relay mode host, port and sender are the document's, and the credential
// is the relay Secret's when it holds both keys, otherwise none.
func relayMode(ctx context.Context, cli kubernetes.Interface) (Settings, bool) {
	cm, err := cli.CoreV1().ConfigMaps(upstream.Namespace).Get(ctx, upstream.Name, metav1.GetOptions{})
	if err != nil || cm == nil {
		return Settings{}, false
	}
	v := func(k string) string { return strings.TrimSpace(cm.Data[k]) }
	if v("MAIL_MODE") != "relay" {
		return Settings{}, false
	}
	s := Settings{Host: v("MAIL_RELAY_HOST"), Port: v("MAIL_RELAY_PORT"), From: v("MAIL_FROM")}
	if s.Port == "" {
		s.Port = defaultPort
	}
	name := v("MAIL_RELAY_SECRET")
	if name == "" {
		return s, true
	}
	sec, err := cli.CoreV1().Secrets(Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil || sec == nil {
		return s, true
	}
	user := strings.TrimSpace(string(sec.Data["smtp-user"]))
	pass := strings.TrimSpace(string(sec.Data["smtp-pass"]))
	if user != "" && pass != "" {
		s.User, s.Pass = user, pass
	}
	return s, true
}

// fromSecret reads the keys smtp-host, smtp-port, smtp-from, smtp-user and
// smtp-pass. The Secret wins only as a whole: without its host, user and
// password the environment answers.
func fromSecret(data map[string][]byte, env Settings) Settings {
	v := func(k string) string { return strings.TrimSpace(string(data[k])) }
	s := Settings{Host: v("smtp-host"), Port: v("smtp-port"), From: v("smtp-from"), User: v("smtp-user"), Pass: v("smtp-pass")}
	if s.Host == "" || s.User == "" || s.Pass == "" {
		return env
	}
	if s.Port == "" {
		s.Port = defaultPort
	}
	if s.From == "" {
		s.From = env.From
	}
	return s
}

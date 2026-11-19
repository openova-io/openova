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
package signinmail

import (
	"context"
	"os"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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
// in-cluster identity. It never mixes the two credentials.
func Resolve(ctx context.Context) Settings {
	env := FromEnv()
	cli, err := ClientFor()
	if err != nil || cli == nil {
		return env
	}
	ctx, cancel := context.WithTimeout(ctx, readBudget)
	defer cancel()
	sec, err := cli.CoreV1().Secrets(Namespace).Get(ctx, ObjectName, metav1.GetOptions{})
	if err != nil || sec == nil {
		return env
	}
	return fromSecret(sec.Data, env)
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

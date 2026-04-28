// Package bootstrap — helper functions that exec helm/kubectl against the
// remote cluster via the kubeconfig the Hetzner provisioner returned.
//
// We exec the binaries rather than vendoring helm-go + client-go because:
//  1. Binary size — vendoring those packages doubles the catalyst-api image
//  2. Stability — helm/kubectl CLI semantics are stable across versions
//  3. Operator familiarity — a sovereign-admin can copy the exact command
//     lines from the wizard's progress UI to debug interactively
//
// The kubeconfig is written to a temp file with mode 0600 for each exec
// and removed afterwards. We never set KUBECONFIG in the process env;
// every exec gets `--kubeconfig=<tmp>` instead, so concurrent provisioning
// runs against different Sovereigns don't race on a shared env var.
package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// runHelm runs `helm <args...>` against the given kubeconfig. If valuesYaml
// is non-empty, it is piped to helm via STDIN (used with `helm install ...
// --values -`).
func runHelm(ctx context.Context, kubeconfig string, action string, args []string, valuesYaml string) error {
	kc, cleanup, err := writeKubeconfig(kubeconfig)
	if err != nil {
		return err
	}
	defer cleanup()

	full := append([]string{action}, args...)
	full = append(full, "--kubeconfig="+kc)

	cmd := exec.CommandContext(ctx, "helm", full...)
	if valuesYaml != "" {
		cmd.Stdin = strings.NewReader(valuesYaml)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("helm %s: %w (stderr=%s)", strings.Join(full, " "), err, stderr.String())
	}
	return nil
}

// applyManifest pipes the manifest YAML to `kubectl apply -f -`.
func applyManifest(ctx context.Context, kubeconfig, yaml string) error {
	kc, cleanup, err := writeKubeconfig(kubeconfig)
	if err != nil {
		return err
	}
	defer cleanup()

	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig="+kc, "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl apply: %w (stderr=%s)", err, stderr.String())
	}
	return nil
}

// waitForDeployment polls `kubectl rollout status deployment/<name>` in the
// namespace until it reports rolled out, or the deadline fires.
func waitForDeployment(ctx context.Context, kubeconfig, namespace, name string, timeout time.Duration) error {
	kc, cleanup, err := writeKubeconfig(kubeconfig)
	if err != nil {
		return err
	}
	defer cleanup()

	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("deployment %s/%s did not become ready within %s", namespace, name, timeout)
		}
		cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig="+kc, "-n", namespace, "rollout", "status",
			"deployment/"+name, "--timeout=30s")
		if err := cmd.Run(); err == nil {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
}

// writeKubeconfig writes the kubeconfig to a temp file with mode 0600 and
// returns the path + cleanup func.
func writeKubeconfig(content string) (string, func(), error) {
	f, err := os.CreateTemp("", "kubeconfig-*.yaml")
	if err != nil {
		return "", nil, fmt.Errorf("create temp kubeconfig: %w", err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("write kubeconfig: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("close kubeconfig: %w", err)
	}
	if err := os.Chmod(f.Name(), 0o600); err != nil {
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("chmod kubeconfig: %w", err)
	}
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

// Package remotekeyscope evaluates the admission rule
// external-secret-remote-key-scope of bp-external-secrets-stores (SEC-080)
// with the Kubernetes admission CEL evaluator, without a cluster.
//
// The test renders the chart platform/external-secrets-stores/chart with
// helm, reads the ValidatingAdmissionPolicy from the render, and compiles it
// the way the API server does: the same composition environment, the same
// variables and validations, the same validator (k8s.io/apiserver
// pkg/admission/plugin/policy/validating, compilePolicy). It then evaluates
// ExternalSecrets and PushSecrets and asserts the decision of each.
//
// A render test checks only the shape of the YAML. A rule that does not
// compile, or that refuses every request, renders fine. This test sees both.
//
// Why here: this is the one Go module of the repository whose dependencies
// already hold k8s.io/apiserver (v0.31, the minor version of the k3s release
// the Sovereigns run). Nothing in the webhook uses this package. The workflow
// remote-key-scope-cel.yaml runs it when the chart changes.
package remotekeyscope

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apiserver/pkg/admission"
	plugincel "k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/cel/environment"
)

const (
	policyName = "external-secret-remote-key-scope"
	release    = "external-secrets-stores"
	releaseNS  = "external-secrets-system"
	template   = "templates/remote-key-scope-policy.yaml"
	// chartDir is the chart, from the folder of this package.
	chartDir = "../../../../platform/external-secrets-stores/chart"
)

// The three renders the tests use. Every argument is a constant.
func renderDefault(t *testing.T) (*admissionregistrationv1.ValidatingAdmissionPolicy, *admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
	return render(t, exec.Command("helm", "template", release, chartDir, "--namespace", releaseNS, "--show-only", template))
}

func renderAddedNamespace(t *testing.T) (*admissionregistrationv1.ValidatingAdmissionPolicy, *admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
	return render(t, exec.Command("helm", "template", release, chartDir, "--namespace", releaseNS, "--show-only", template,
		"--set-json", `legacyKeyMap={"team-a":["sso/"]}`))
}

func renderEmptyMap(t *testing.T) (*admissionregistrationv1.ValidatingAdmissionPolicy, *admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
	return render(t, exec.Command("helm", "template", release, chartDir, "--namespace", releaseNS, "--show-only", template,
		"--set-json", "legacyKeyMap=null"))
}

// render runs a helm template command and returns the policy and its binding.
func render(t *testing.T, cmd *exec.Cmd) (*admissionregistrationv1.ValidatingAdmissionPolicy, *admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
	t.Helper()
	if cmd.Err != nil {
		t.Fatalf("helm is not on PATH; this test renders the chart with it: %v", cmd.Err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v\n%s", cmd.String(), err, stderr.String())
	}
	return decode(t, out)
}

// decode reads the policy and the binding of policyName from a multi-document YAML stream.
func decode(t *testing.T, stream []byte) (*admissionregistrationv1.ValidatingAdmissionPolicy, *admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
	t.Helper()
	var pol *admissionregistrationv1.ValidatingAdmissionPolicy
	var bind *admissionregistrationv1.ValidatingAdmissionPolicyBinding
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(stream), 4096)
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("the render is not YAML: %v", err)
		}
		if doc == nil {
			continue
		}
		u := unstructured.Unstructured{Object: doc}
		if u.GetName() != policyName {
			continue
		}
		var err error
		switch u.GetKind() {
		case "ValidatingAdmissionPolicy":
			pol = &admissionregistrationv1.ValidatingAdmissionPolicy{}
			err = runtime.DefaultUnstructuredConverter.FromUnstructuredWithValidation(doc, pol, true)
		case "ValidatingAdmissionPolicyBinding":
			bind = &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
			err = runtime.DefaultUnstructuredConverter.FromUnstructuredWithValidation(doc, bind, true)
		}
		if err != nil {
			t.Fatalf("%s %s does not decode as admissionregistration.k8s.io/v1: %v", u.GetKind(), policyName, err)
		}
	}
	if pol == nil || bind == nil {
		t.Fatalf("the render holds no ValidatingAdmissionPolicy or no binding named %s", policyName)
	}
	return pol, bind
}

// compiled is a policy compiled as compilePolicy of the validating admission plugin does it.
type compiled struct {
	validator validating.Validator
	errs      []string // compile errors of variables and validations
}

func compile(pol *admissionregistrationv1.ValidatingAdmissionPolicy, strictCost bool) compiled {
	hasParam := pol.Spec.ParamKind != nil
	optionalVars := plugincel.OptionalVariableDeclarations{HasParams: hasParam, HasAuthorizer: true, StrictCost: strictCost}
	messageVars := plugincel.OptionalVariableDeclarations{HasParams: hasParam, HasAuthorizer: false, StrictCost: strictCost}
	env, err := plugincel.NewCompositionEnv(plugincel.VariablesTypeName, environment.MustBaseEnvSet(environment.DefaultCompatibilityVersion(), strictCost))
	if err != nil {
		return compiled{errs: []string{"composition environment: " + err.Error()}}
	}
	c := plugincel.NewCompositedCompilerFromTemplate(env)
	var errs []string
	for _, v := range pol.Spec.Variables {
		res := c.CompileAndStoreVariable(&validating.Variable{Name: v.Name, Expression: v.Expression}, optionalVars, environment.StoredExpressions)
		if res.Error != nil {
			errs = append(errs, "variable "+v.Name+": "+res.Error.Error())
		}
	}
	validations := make([]plugincel.ExpressionAccessor, len(pol.Spec.Validations))
	messages := make([]plugincel.ExpressionAccessor, len(pol.Spec.Validations))
	for i, v := range pol.Spec.Validations {
		cond := &validating.ValidationCondition{Expression: v.Expression, Message: v.Message, Reason: v.Reason}
		if res := c.CompileCELExpression(cond, optionalVars, environment.StoredExpressions); res.Error != nil {
			errs = append(errs, "validation "+v.Expression+": "+res.Error.Error())
		}
		validations[i] = cond
		if v.MessageExpression != "" {
			messages[i] = &validating.MessageExpressionCondition{MessageExpression: v.MessageExpression}
		}
	}
	return compiled{
		validator: validating.NewValidator(
			c.Compile(validations, optionalVars, environment.StoredExpressions),
			nil,
			c.Compile(nil, optionalVars, environment.StoredExpressions),
			c.Compile(messages, messageVars, environment.StoredExpressions),
			pol.Spec.FailurePolicy,
		),
		errs: errs,
	}
}

var (
	gvkExternalSecret = schema.GroupVersionKind{Group: "external-secrets.io", Version: "v1beta1", Kind: "ExternalSecret"}
	gvrExternalSecret = schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1beta1", Resource: "externalsecrets"}
	gvkPushSecret     = schema.GroupVersionKind{Group: "external-secrets.io", Version: "v1alpha1", Kind: "PushSecret"}
	gvrPushSecret     = schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1alpha1", Resource: "pushsecrets"}
)

// externalSecret is an ExternalSecret in ns with one data entry per key and the given dataFrom entries.
func externalSecret(ns string, keys []string, dataFrom ...map[string]any) *unstructured.Unstructured {
	spec := map[string]any{
		"secretStoreRef": map[string]any{"kind": "ClusterSecretStore", "name": "vault-region1"},
		"target":         map[string]any{"name": "probe"},
	}
	if len(keys) > 0 {
		data := []any{}
		for i, k := range keys {
			data = append(data, map[string]any{"secretKey": "k" + string(rune('a'+i)), "remoteRef": map[string]any{"key": k}})
		}
		spec["data"] = data
	}
	if len(dataFrom) > 0 {
		df := []any{}
		for _, f := range dataFrom {
			df = append(df, f)
		}
		spec["dataFrom"] = df
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvkExternalSecret.GroupVersion().String(), "kind": gvkExternalSecret.Kind,
		"metadata": map[string]any{"name": "probe", "namespace": ns},
		"spec":     spec,
	}}
}

// pushSecret is a PushSecret in ns with one data entry per remote key.
func pushSecret(ns string, keys ...string) *unstructured.Unstructured {
	data := []any{}
	for _, k := range keys {
		data = append(data, map[string]any{"match": map[string]any{"secretKey": "v", "remoteRef": map[string]any{"remoteKey": k}}})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvkPushSecret.GroupVersion().String(), "kind": gvkPushSecret.Kind,
		"metadata": map[string]any{"name": "probe", "namespace": ns},
		"spec": map[string]any{
			"secretStoreRefs": []any{map[string]any{"kind": "ClusterSecretStore", "name": "vault-region1"}},
			"selector":        map[string]any{"secret": map[string]any{"name": "probe"}},
			"data":            data,
		},
	}}
}

// decide evaluates the compiled policy on a create of obj and returns its one decision.
func decide(t *testing.T, c compiled, obj *unstructured.Unstructured) validating.PolicyDecision {
	t.Helper()
	gvk, gvr := gvkExternalSecret, gvrExternalSecret
	if obj.GetKind() == gvkPushSecret.Kind {
		gvk, gvr = gvkPushSecret, gvrPushSecret
	}
	ns := obj.GetNamespace()
	attr := admission.NewAttributesRecord(obj, nil, gvk, ns, obj.GetName(), gvr, "", admission.Create,
		&metav1.CreateOptions{}, false, &user.DefaultInfo{Name: "system:serviceaccount:flux-system:helm-controller"})
	versioned := &admission.VersionedAttributes{Attributes: attr, VersionedKind: gvk, VersionedObject: obj}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
	res := c.validator.Validate(context.Background(), gvr, versioned, nil, namespace, celconfig.RuntimeCELCostBudget, nil)
	if len(res.Decisions) != 1 {
		t.Fatalf("%d decisions, want 1: %+v", len(res.Decisions), res.Decisions)
	}
	return res.Decisions[0]
}

type evalCase struct {
	name  string
	obj   *unstructured.Unstructured
	admit bool
}

// defaultCases run against the render with the chart's default values.
func defaultCases() []evalCase {
	return []evalCase{
		{"own apps prefix is admitted", externalSecret("catalyst", []string{"openova/apps/catalyst/db"}), true},
		{"own namespace prefix is admitted", externalSecret("catalyst", []string{"catalyst/db"}), true},
		{"legacy prefix of the namespace is admitted (gitea, sso/)", externalSecret("gitea", []string{"sso/sec-g07-probe"}), true},
		{"both legacy prefixes of newapi are admitted", externalSecret("newapi", []string{"catalyst/newapi-db", "sso/newapi"}), true},
		{"another namespace's legacy prefix is refused (gitea, catalyst/)", externalSecret("gitea", []string{"catalyst/tofu-phase0-archive"}), false},
		{"another namespace's own apps prefix is refused", externalSecret("gitea", []string{"openova/apps/harbor/admin"}), false},
		{"another namespace's own prefix is refused", externalSecret("gitea", []string{"harbor/admin"}), false},
		{"a key that only begins like the namespace is refused", externalSecret("gitea", []string{"gitea-other/x"}), false},
		{"a namespace not in the map with a foreign key is refused", externalSecret("default", []string{"sso/sec-g07-probe"}), false},
		{"one good and one foreign key is refused", externalSecret("gitea", []string{"sso/gitea", "catalyst/x"}), false},
		{"dataFrom find without a path is refused", externalSecret("gitea", nil,
			map[string]any{"find": map[string]any{"name": map[string]any{"regexp": ".*"}}}), false},
		{"dataFrom find with an empty path is refused", externalSecret("gitea", nil,
			map[string]any{"find": map[string]any{"path": "", "name": map[string]any{"regexp": ".*"}}}), false},
		{"dataFrom find under the own prefix is admitted", externalSecret("gitea", nil,
			map[string]any{"find": map[string]any{"path": "gitea/", "name": map[string]any{"regexp": ".*"}}}), true},
		{"dataFrom extract of a foreign key is refused", externalSecret("default", nil,
			map[string]any{"extract": map[string]any{"key": "catalyst/x"}}), false},
		{"dataFrom extract of a legacy key is admitted", externalSecret("openbao", nil,
			map[string]any{"extract": map[string]any{"key": "sso/openbao"}}), true},
		{"a generator dataFrom (neither extract nor find) is admitted", externalSecret("default", nil,
			map[string]any{"sourceRef": map[string]any{"generatorRef": map[string]any{
				"apiVersion": "generators.external-secrets.io/v1alpha1", "kind": "Password", "name": "pw"}}}), true},
		{"PushSecret to a legacy key is admitted", pushSecret("openbao", "sso/openbao"), true},
		{"PushSecret to a foreign key is refused", pushSecret("openbao", "catalyst/x"), false},
	}
}

func check(t *testing.T, c compiled, cases []evalCase, message string) {
	t.Helper()
	if len(c.errs) > 0 {
		t.Fatalf("the rendered policy does not compile:\n  %s", strings.Join(c.errs, "\n  "))
	}
	// A refusal must come from the validation expression, not from a failed
	// evaluation: with failurePolicy Fail both refuse, but only the first is
	// the rule. The validator marks a failed evaluation EvalError and leaves
	// Evaluation empty on a refusal by the expression.
	for _, tc := range cases {
		d := decide(t, c, tc.obj)
		switch {
		case d.Evaluation == validating.EvalError:
			t.Errorf("%s: the evaluation failed (%s), so every such request is refused", tc.name, d.Message)
		case tc.admit && (d.Action != validating.ActionAdmit || d.Evaluation != validating.EvalAdmit):
			t.Errorf("%s: refused, want admitted: %s", tc.name, d.Message)
		case !tc.admit && d.Action != validating.ActionDeny:
			t.Errorf("%s: admitted, want refused", tc.name)
		case !tc.admit && d.Message != message:
			t.Errorf("%s: message %q, want the policy's message %q", tc.name, d.Message, message)
		}
	}
}

// TestRemoteKeyScopeEvaluates evaluates the render with the default values,
// with and without strict cost enforcement (a feature gate of the API server).
func TestRemoteKeyScopeEvaluates(t *testing.T) {
	pol, bind := renderDefault(t)
	if pol.Spec.ParamKind != nil {
		t.Fatalf("the policy has paramKind %+v; it must read no parameter", pol.Spec.ParamKind)
	}
	if bind.Spec.ParamRef != nil {
		t.Fatalf("the binding has paramRef %+v; it must read no parameter", bind.Spec.ParamRef)
	}
	if len(pol.Spec.Validations) != 1 {
		t.Fatalf("%d validations, want 1", len(pol.Spec.Validations))
	}
	for _, strict := range []bool{false, true} {
		name := "strictCost=false"
		if strict {
			name = "strictCost=true"
		}
		t.Run(name, func(t *testing.T) {
			check(t, compile(pol, strict), defaultCases(), pol.Spec.Validations[0].Message)
		})
	}
}

// TestRemoteKeyScopeValues evaluates renders with other values: an added
// namespace, and an empty map, which renders the literal {}.
func TestRemoteKeyScopeValues(t *testing.T) {
	pol, _ := renderAddedNamespace(t)
	check(t, compile(pol, true), []evalCase{
		{"the added namespace's prefix is admitted", externalSecret("team-a", []string{"sso/team-a"}), true},
		{"the default entries stay (gitea, sso/)", externalSecret("gitea", []string{"sso/gitea"}), true},
		{"the added namespace gets no other prefix", externalSecret("team-a", []string{"catalyst/x"}), false},
	}, pol.Spec.Validations[0].Message)

	pol, _ = renderEmptyMap(t)
	check(t, compile(pol, true), []evalCase{
		{"with an empty map a legacy key is refused", externalSecret("gitea", []string{"sso/gitea"}), false},
		{"with an empty map the own prefix is admitted", externalSecret("gitea", []string{"gitea/x"}), true},
		{"with an empty map the own apps prefix is admitted", externalSecret("gitea", []string{"openova/apps/gitea/x"}), true},
	}, pol.Spec.Validations[0].Message)
}

// TestRemoteKeyScopeHarnessCanFail proves that the harness sees a rule that
// admits too much and a rule that does not compile. Without it, a harness that
// admits or refuses everything would pass half of the cases above.
func TestRemoteKeyScopeHarnessCanFail(t *testing.T) {
	pol, _ := renderDefault(t)

	open := pol.DeepCopy()
	open.Spec.Validations[0].Expression = "true"
	c := compile(open, true)
	if len(c.errs) > 0 {
		t.Fatalf("the open rule does not compile: %v", c.errs)
	}
	if d := decide(t, c, externalSecret("default", []string{"sso/sec-g07-probe"})); d.Action != validating.ActionAdmit {
		t.Fatalf("a rule that admits everything refused a foreign key: %+v", d)
	}

	broken := pol.DeepCopy()
	found := false
	for i, v := range broken.Spec.Variables {
		if v.Name == "legacyMap" && strings.HasSuffix(v.Expression, "}") {
			broken.Spec.Variables[i].Expression = strings.TrimSuffix(v.Expression, "}")
			found = true
		}
	}
	if !found {
		t.Fatalf("the policy has no variable legacyMap that holds a map literal")
	}
	c = compile(broken, true)
	if len(c.errs) == 0 {
		t.Fatalf("a map literal without its closing brace compiled")
	}
	d := decide(t, c, externalSecret("gitea", []string{"sso/gitea"}))
	if d.Action != validating.ActionDeny || d.Evaluation != validating.EvalError {
		t.Fatalf("a rule that does not compile was not an evaluation error that refuses: %+v", d)
	}
}

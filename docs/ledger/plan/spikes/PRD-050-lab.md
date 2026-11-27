# PRD-050 lab: probe, scripts and output

The experiment behind [PRD-050.md](PRD-050.md). Nothing here runs on a cluster or in CI.

- `prd050_probe_test.go` goes into a clone of cilium at tag v1.19.3 and runs Cilium's own Gateway API ingestion and translator on a console Gateway shaped as NET-191 writes it. `TestPRD050Probe` asserts what the generated Envoy configuration holds (questions Q1 to Q5). `TestPRD050Bootstrap` writes that configuration as a static Envoy bootstrap, `cfg/gateway.json`.
- `up.sh` loads the bootstrap into the pinned cilium-envoy binary, beside a second cilium-envoy with three lab back ends (`cfg/backend.yaml`), in one rootless podman pod published on 127.0.0.1 only. `cases.sh` calls it with real TLS clients. `down.sh` removes the pod.

To repeat, put the files below in one directory `<lab>` (the YAML file under `<lab>/cfg/`), then:

```sh
git clone --depth 1 --branch v1.19.3 https://github.com/cilium/cilium.git <lab>/cilium
podman pull quay.io/cilium/cilium-envoy@sha256:ba0ab8adac082d50d525fd2c5ba096c8facea3a471561b7c61c7a5b9c2e0de0d
sh <lab>/all.sh
```

The Go toolchain must allow Cilium's `go 1.25.0` (with `GOTOOLCHAIN=auto` it fetches it). Outputs below are from one run of `all.sh` on 27 November 2026; the lab directory is shown as `<lab>`.

## Files

### `prd050_probe_test.go`

```go
// PRD-050 probe: what Cilium v1.19.3's own Gateway API ingestion and
// translator produce for an Organization listener that asks for client
// certificate validation. Copied into a scratch clone of cilium at tag v1.19.3,
// file operator/pkg/model/translation/gateway-api/prd050_probe_test.go, and run
// with (render.sh): PRD050_BOOTSTRAP=<file> go test -count=1 -run 'TestPRD050(Probe|Bootstrap)$' -v ./operator/pkg/model/translation/gateway-api/
package gateway_api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	envoy_bootstrap "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	envoy_cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoy_core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoy_endpoint "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoy_listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoy_route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoy_hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	envoy_tcp "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/tcp_proxy/v3"
	envoy_tls "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	"github.com/cilium/cilium/operator/pkg/model"
	"github.com/cilium/cilium/operator/pkg/model/ingestion"
	"github.com/cilium/cilium/operator/pkg/model/translation"
	ciliumv2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
)

// The console Gateway as NET-191 writes it (apex listener plus one HTTPS
// listener per Organization, all on one port), in the v1.3.0 experimental
// shape the kit installs, with listener-level frontendValidation on org1 and a
// TLS passthrough listener for one host org1 owns.
const probeGateway = `
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: console, namespace: kube-system}
spec:
  gatewayClassName: cilium
  listeners:
  - name: console-https
    hostname: console.sov.example.test
    port: 443
    protocol: HTTPS
    tls: {mode: Terminate, certificateRefs: [{kind: Secret, name: console-tls}]}
  - name: console-https-org1
    hostname: "*.org1.example.test"
    port: 443
    protocol: HTTPS
    allowedRoutes:
      namespaces:
        from: Selector
        selector: {matchExpressions: [{key: kubernetes.io/metadata.name, operator: In, values: [org1, catalyst]}]}
    tls:
      mode: Terminate
      certificateRefs: [{kind: Secret, name: org1-wildcard-tls}]
      frontendValidation:
        caCertificateRefs: [{group: "", kind: ConfigMap, name: org1-client-ca}]
  - name: console-https-org2
    hostname: "*.org2.example.test"
    port: 443
    protocol: HTTPS
    allowedRoutes:
      namespaces:
        from: Selector
        selector: {matchExpressions: [{key: kubernetes.io/metadata.name, operator: In, values: [org2, catalyst]}]}
    tls: {mode: Terminate, certificateRefs: [{kind: Secret, name: org2-wildcard-tls}]}
  - name: console-tls-org1-api
    hostname: api.org1.example.test
    port: 443
    protocol: TLS
    allowedRoutes:
      kinds: [{kind: TLSRoute}]
      namespaces:
        from: Selector
        selector: {matchExpressions: [{key: kubernetes.io/metadata.name, operator: In, values: [org1]}]}
    tls: {mode: Passthrough}
`

const probeRoutes = `
- apiVersion: gateway.networking.k8s.io/v1
  kind: HTTPRoute
  metadata: {name: web, namespace: org1}
  spec:
    parentRefs: [{name: console, namespace: kube-system, sectionName: console-https-org1}]
    hostnames: [www.org1.example.test]
    rules: [{backendRefs: [{name: web, port: 80}]}]
- apiVersion: gateway.networking.k8s.io/v1
  kind: HTTPRoute
  metadata: {name: web, namespace: org2}
  spec:
    parentRefs: [{name: console, namespace: kube-system, sectionName: console-https-org2}]
    hostnames: [www.org2.example.test]
    rules: [{backendRefs: [{name: web, port: 80}]}]
`

const probeTLSRoute = `
apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TLSRoute
metadata: {name: api, namespace: org1}
spec:
  parentRefs: [{name: console, namespace: kube-system, sectionName: console-tls-org1-api}]
  hostnames: [api.org1.example.test]
  rules: [{backendRefs: [{name: fingate-api, port: 8443}]}]
`

func probeService(ns, name string, port int32) corev1.Service {
	s := corev1.Service{}
	s.Namespace, s.Name = ns, name
	s.Spec.Ports = []corev1.ServicePort{{Port: port}}
	return s
}

// probeRender runs the probe objects through Cilium's own ingestion and
// translator, as gateway_reconcile.go does, and returns the CiliumEnvoyConfig.
func probeRender(t *testing.T) *ciliumv2.CiliumEnvoyConfig {
	// Q1: the operator reads Gateways into the vendored (v1.4.0-rc.2) Go
	// types. Decode the v1.3.0-shaped object the way a non-strict JSON decode
	// does, and look for the listener field after a round trip.
	var gw gatewayv1.Gateway
	if err := yaml.Unmarshal([]byte(probeGateway), &gw); err != nil {
		t.Fatal(err)
	}
	round, _ := json.Marshal(gw)
	if !strings.Contains(string(round), "console-https-org1") || !strings.Contains(string(round), "org1-wildcard-tls") {
		t.Fatalf("Q1: the decode lost the listener itself, so its absence proves nothing: %s", round)
	}
	if strings.Contains(string(round), "frontendValidation") || strings.Contains(string(round), "org1-client-ca") {
		t.Errorf("Q1: the listener frontendValidation survived the decode: %s", round)
	} else {
		t.Logf("Q1 decode: listener console-https-org1 and its certificateRefs decoded; tls.frontendValidation is absent after decoding into the operator's Gateway type")
	}

	// Q1b: the Gateway-level field of the newer API, which the vendored types
	// do carry, set on the typed object directly.
	gw.Spec.TLS = &gatewayv1.GatewayTLSConfig{Frontend: &gatewayv1.FrontendTLSConfig{
		Default: gatewayv1.TLSConfig{Validation: &gatewayv1.FrontendTLSValidation{
			CACertificateRefs: []gatewayv1.ObjectReference{{Kind: "ConfigMap", Name: "org1-client-ca"}},
		}},
	}}

	var routes []gatewayv1.HTTPRoute
	if err := yaml.Unmarshal([]byte(probeRoutes), &routes); err != nil {
		t.Fatal(err)
	}
	var tlsRoute gatewayv1alpha2.TLSRoute
	if err := yaml.Unmarshal([]byte(probeTLSRoute), &tlsRoute); err != nil {
		t.Fatal(err)
	}
	gwc := gatewayv1.GatewayClass{}
	gwc.Name = "cilium"
	gwc.Spec.ControllerName = "io.cilium/gateway-controller"

	httpL, tlsL := ingestion.GatewayAPI(ingestion.Input{
		GatewayClass: gwc,
		Gateway:      gw,
		HTTPRoutes:   routes,
		TLSRoutes:    []gatewayv1alpha2.TLSRoute{tlsRoute},
		Services: []corev1.Service{
			probeService("org1", "web", 80), probeService("org2", "web", 80), probeService("org1", "fingate-api", 8443),
		},
	})
	t.Logf("ingestion: %d HTTP listeners, %d TLS passthrough listeners", len(httpL), len(tlsL))

	// The kit's Huawei shape: gateway on the host network.
	cfg := translation.Config{
		SecretsNamespace:  "cilium-secrets",
		HostNetworkConfig: translation.HostNetworkConfig{Enabled: true},
		IPConfig:          translation.IPConfig{IPv4Enabled: true},
	}
	trans := &gatewayAPITranslator{cecTranslator: translation.NewCECTranslator(cfg), cfg: cfg}
	cec, svc, _, err := trans.Translate(&model.Model{HTTP: httpL, TLSPassthrough: tlsL})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("service %s/%s type=%s", svc.Namespace, svc.Name, svc.Spec.Type)
	return cec
}

func TestPRD050Probe(t *testing.T) {
	cec := probeRender(t)
	hcms := 0
	routeTables := map[string][]string{}
	terminating, rdsNames := 0, map[string]bool{}
	sawPassthrough := false
	for _, r := range cec.Spec.Resources {
		switch {
		case strings.HasSuffix(r.Any.TypeUrl, "envoy.config.route.v3.RouteConfiguration"):
			rc := &envoy_route.RouteConfiguration{}
			if err := r.Any.UnmarshalTo(rc); err != nil {
				t.Fatal(err)
			}
			for _, vh := range rc.VirtualHosts {
				routeTables[rc.Name] = append(routeTables[rc.Name], vh.Domains...)
			}
		case strings.HasSuffix(r.Any.TypeUrl, "envoy.config.listener.v3.Listener"):
			l := &envoy_listener.Listener{}
			if err := r.Any.UnmarshalTo(l); err != nil {
				t.Fatal(err)
			}
			for _, fc := range l.FilterChains {
				sni := fc.GetFilterChainMatch().GetServerNames()
				if ts := fc.GetTransportSocket(); ts != nil {
					terminating++
					dtc := &envoy_tls.DownstreamTlsContext{}
					if err := ts.GetTypedConfig().UnmarshalTo(dtc); err != nil {
						t.Fatal(err)
					}
					req := dtc.GetRequireClientCertificate().GetValue()
					vc := dtc.GetCommonTlsContext().GetValidationContextType() != nil
					t.Logf("Q2 chain sni=%v require_client_certificate=%v validation_context=%v", sni, req, vc)
					if req || vc {
						t.Errorf("Q2: chain %v validates client certificates", sni)
					}
				}
				for _, f := range fc.Filters {
					switch f.Name {
					case "envoy.filters.network.http_connection_manager":
						h := &envoy_hcm.HttpConnectionManager{}
						if err := f.GetTypedConfig().UnmarshalTo(h); err != nil {
							t.Fatal(err)
						}
						if fc.GetTransportSocket() != nil {
							rdsNames[h.GetRds().GetRouteConfigName()] = true
						}
						t.Logf("Q3 chain sni=%v hcm=%s rds=%s forward_client_cert_details=%s set_current_client_cert_details=%v",
							sni, h.StatPrefix, h.GetRds().GetRouteConfigName(), h.ForwardClientCertDetails, h.SetCurrentClientCertDetails != nil)
						hcms++
						if h.ForwardClientCertDetails != envoy_hcm.HttpConnectionManager_SANITIZE || h.SetCurrentClientCertDetails != nil {
							t.Errorf("Q3: chain %v forwards client certificate details: %s", sni, h.ForwardClientCertDetails)
						}
					case "envoy.filters.network.tcp_proxy":
						tp := &envoy_tcp.TcpProxy{}
						if err := f.GetTypedConfig().UnmarshalTo(tp); err != nil {
							t.Fatal(err)
						}
						t.Logf("Q5 chain sni=%v transport_socket=%v tcp_proxy cluster=%s", sni, fc.GetTransportSocket() != nil, tp.GetCluster())
						if len(sni) == 1 && sni[0] == "api.org1.example.test" && fc.GetTransportSocket() == nil && strings.Contains(tp.GetCluster(), "org1:fingate-api:8443") {
							sawPassthrough = true
						}
					}
				}
			}
		}
	}
	if hcms != 4 {
		t.Errorf("Q3: want 4 HTTP connection managers (plain, apex, org1, org2), got %d", hcms)
	}
	if terminating != 3 {
		t.Errorf("Q2: want 3 TLS-terminating chains (apex, org1, org2), got %d", terminating)
	}
	if len(rdsNames) != 1 {
		t.Errorf("Q4: want one route table shared by every terminating chain, got %v", rdsNames)
	}
	for name := range rdsNames {
		d := strings.Join(routeTables[name], ",")
		t.Logf("Q4 route table %s domains=%s", name, d)
		if !strings.Contains(d, "www.org1.example.test") || !strings.Contains(d, "www.org2.example.test") {
			t.Errorf("Q4: route table %s does not hold both Organizations' hosts: %s", name, d)
		}
	}
	if !sawPassthrough {
		t.Errorf("Q5: no passthrough chain for api.org1.example.test to org1/fingate-api:8443")
	}
}

// TestPRD050Bootstrap writes the rendered listener as a static Envoy
// bootstrap for the pinned cilium-envoy binary. Only the transport is
// replaced: each HTTP connection manager's RDS name becomes the same route
// table inlined, each EDS cluster a static cluster on 127.0.0.1, and each SDS
// secret a static secret of the same name read from /cfg.
func TestPRD050Bootstrap(t *testing.T) {
	out := os.Getenv("PRD050_BOOTSTRAP")
	if out == "" {
		t.Fatal("PRD050_BOOTSTRAP must name the bootstrap file to write")
	}
	cec := probeRender(t)
	backendPort := map[string]uint32{"org1:web:80": 8081, "org2:web:80": 8082, "org1:fingate-api:8443": 8443}
	routes := map[string]*envoy_route.RouteConfiguration{}
	var listeners []*envoy_listener.Listener
	var clusterNames []string
	for _, r := range cec.Spec.Resources {
		switch {
		case strings.HasSuffix(r.Any.TypeUrl, "envoy.config.route.v3.RouteConfiguration"):
			rc := &envoy_route.RouteConfiguration{}
			if err := r.Any.UnmarshalTo(rc); err != nil {
				t.Fatal(err)
			}
			routes[rc.Name] = rc
		case strings.HasSuffix(r.Any.TypeUrl, "envoy.config.listener.v3.Listener"):
			l := &envoy_listener.Listener{}
			if err := r.Any.UnmarshalTo(l); err != nil {
				t.Fatal(err)
			}
			listeners = append(listeners, l)
		case strings.HasSuffix(r.Any.TypeUrl, "envoy.config.cluster.v3.Cluster"):
			c := &envoy_cluster.Cluster{}
			if err := r.Any.UnmarshalTo(c); err != nil {
				t.Fatal(err)
			}
			clusterNames = append(clusterNames, c.Name)
		}
	}
	secrets := map[string]bool{}
	for _, l := range listeners {
		t.Logf("listener %s address=%s:%d filter_chains=%d", l.Name, l.GetAddress().GetSocketAddress().GetAddress(), l.GetAddress().GetSocketAddress().GetPortValue(), len(l.FilterChains))
		for _, fc := range l.FilterChains {
			if ts := fc.GetTransportSocket(); ts != nil {
				dtc := &envoy_tls.DownstreamTlsContext{}
				if err := ts.GetTypedConfig().UnmarshalTo(dtc); err != nil {
					t.Fatal(err)
				}
				for _, sc := range dtc.GetCommonTlsContext().GetTlsCertificateSdsSecretConfigs() {
					secrets[sc.Name] = true
				}
			}
			for _, f := range fc.Filters {
				if f.Name != "envoy.filters.network.http_connection_manager" {
					continue
				}
				h := &envoy_hcm.HttpConnectionManager{}
				if err := f.GetTypedConfig().UnmarshalTo(h); err != nil {
					t.Fatal(err)
				}
				rc := routes[h.GetRds().GetRouteConfigName()]
				if rc == nil {
					t.Fatalf("no rendered route table %q", h.GetRds().GetRouteConfigName())
				}
				h.RouteSpecifier = &envoy_hcm.HttpConnectionManager_RouteConfig{RouteConfig: rc}
				a, err := anypb.New(h)
				if err != nil {
					t.Fatal(err)
				}
				f.ConfigType = &envoy_listener.Filter_TypedConfig{TypedConfig: a}
			}
		}
	}
	b := &envoy_bootstrap.Bootstrap{
		Node:            &envoy_core.Node{Id: "prd050-gateway", Cluster: "prd050"},
		StaticResources: &envoy_bootstrap.Bootstrap_StaticResources{Listeners: listeners},
	}
	for _, name := range clusterNames {
		port, ok := backendPort[name]
		if !ok {
			t.Fatalf("rendered cluster %q has no local backend in the probe", name)
		}
		b.StaticResources.Clusters = append(b.StaticResources.Clusters, &envoy_cluster.Cluster{
			Name:                 name,
			ClusterDiscoveryType: &envoy_cluster.Cluster_Type{Type: envoy_cluster.Cluster_STATIC},
			ConnectTimeout:       durationpb.New(2 * time.Second),
			LoadAssignment: &envoy_endpoint.ClusterLoadAssignment{ClusterName: name, Endpoints: []*envoy_endpoint.LocalityLbEndpoints{{
				LbEndpoints: []*envoy_endpoint.LbEndpoint{{HostIdentifier: &envoy_endpoint.LbEndpoint_Endpoint{Endpoint: &envoy_endpoint.Endpoint{
					Address: &envoy_core.Address{Address: &envoy_core.Address_SocketAddress{SocketAddress: &envoy_core.SocketAddress{
						Address: "127.0.0.1", PortSpecifier: &envoy_core.SocketAddress_PortValue{PortValue: port}}}},
				}}}},
			}}},
		})
	}
	for name := range secrets {
		file := "/cfg/" + strings.TrimPrefix(name, "cilium-secrets/kube-system-")
		b.StaticResources.Secrets = append(b.StaticResources.Secrets, &envoy_tls.Secret{Name: name, Type: &envoy_tls.Secret_TlsCertificate{
			TlsCertificate: &envoy_tls.TlsCertificate{
				CertificateChain: &envoy_core.DataSource{Specifier: &envoy_core.DataSource_Filename{Filename: file + ".crt"}},
				PrivateKey:       &envoy_core.DataSource{Specifier: &envoy_core.DataSource_Filename{Filename: file + ".key"}},
			}}})
	}
	if len(b.StaticResources.Clusters) != 3 || len(b.StaticResources.Secrets) != 3 {
		t.Fatalf("want 3 clusters and 3 secrets, got %d and %d", len(b.StaticResources.Clusters), len(b.StaticResources.Secrets))
	}
	js, err := protojson.MarshalOptions{Indent: "  "}.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, js, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s: %d listeners, %d clusters, %d secrets", out, len(listeners), len(b.StaticResources.Clusters), len(b.StaticResources.Secrets))
}
```

### `cfg/backend.yaml`

```yaml
# Lab back ends for the PRD-050 probe, on the same pinned cilium-envoy binary.
# org1-web and org2-web echo any x-forwarded-client-cert they receive (ALWAYS_FORWARD_ONLY:
# their own connection manager keeps the header instead of stripping it);
# fingate-api terminates mutual TLS against org1-client-ca and reports the peer.
node: {id: prd050-backend, cluster: prd050}
static_resources:
  listeners:
  - name: org1-web
    address: {socket_address: {address: 127.0.0.1, port_value: 8081}}
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: org1-web
          forward_client_cert_details: ALWAYS_FORWARD_ONLY
          http_filters: [{name: envoy.filters.http.router, typed_config: {"@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router}}]
          route_config:
            virtual_hosts:
            - name: all
              domains: ["*"]
              routes:
              - match: {prefix: /}
                direct_response: {status: 200, body: {inline_string: "backend=org1-web\n"}}
                response_headers_to_add:
                - header: {key: x-seen-xfcc, value: "%REQ(x-forwarded-client-cert)%"}
                - header: {key: x-seen-probe, value: "%REQ(x-probe)%"}
  - name: org2-web
    address: {socket_address: {address: 127.0.0.1, port_value: 8082}}
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: org2-web
          forward_client_cert_details: ALWAYS_FORWARD_ONLY
          http_filters: [{name: envoy.filters.http.router, typed_config: {"@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router}}]
          route_config:
            virtual_hosts:
            - name: all
              domains: ["*"]
              routes:
              - match: {prefix: /}
                direct_response: {status: 200, body: {inline_string: "backend=org2-web\n"}}
                response_headers_to_add:
                - header: {key: x-seen-xfcc, value: "%REQ(x-forwarded-client-cert)%"}
  - name: fingate-api
    address: {socket_address: {address: 127.0.0.1, port_value: 8443}}
    filter_chains:
    - transport_socket:
        name: envoy.transport_sockets.tls
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext
          require_client_certificate: true
          common_tls_context:
            tls_certificates: [{certificate_chain: {filename: /cfg/fingate-api.crt}, private_key: {filename: /cfg/fingate-api.key}}]
            validation_context: {trusted_ca: {filename: /cfg/org1-client-ca.crt}}
      filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: fingate-api
          http_filters: [{name: envoy.filters.http.router, typed_config: {"@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router}}]
          route_config:
            virtual_hosts:
            - name: all
              domains: ["*"]
              routes:
              - match: {prefix: /}
                direct_response: {status: 200, body: {inline_string: "backend=fingate-api\n"}}
                response_headers_to_add:
                - header: {key: x-peer-subject, value: "%DOWNSTREAM_PEER_SUBJECT%"}
```

### `certs.sh`

```sh
#!/bin/sh
# Throwaway lab authorities and leaf certificates (2-day validity) in ./cfg.
set -eu
L=$(cd "$(dirname "$0")" && pwd)
cd "$L/cfg"
ca() { openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj "/CN=$1" -keyout "$1.key" -out "$1.crt" 2>/dev/null; }
leaf() {
  openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=$2" -keyout "$1.key" -out "$1.csr" 2>/dev/null
  printf 'extendedKeyUsage=%s\nsubjectAltName=DNS:%s\n' "$4" "$2" > "$1.ext"
  openssl x509 -req -in "$1.csr" -CA "$3.crt" -CAkey "$3.key" -CAcreateserial -days 2 -extfile "$1.ext" -out "$1.crt" 2>/dev/null
}
ca server-ca
ca org1-client-ca
ca other-ca
leaf console-tls console.sov.example.test server-ca serverAuth
leaf org1-wildcard-tls '*.org1.example.test' server-ca serverAuth
leaf org2-wildcard-tls '*.org2.example.test' server-ca serverAuth
leaf fingate-api api.org1.example.test server-ca serverAuth
leaf tpp-good tpp-good org1-client-ca clientAuth
leaf tpp-wrong tpp-wrong other-ca clientAuth
chmod 0644 ./*.key
```

### `render.sh`

```sh
#!/bin/sh
# Run the probe inside the clone of cilium v1.19.3 in ./cilium; writes cfg/gateway.json.
export KUBECONFIG=/dev/null NO_COLOR=1 GOTOOLCHAIN=auto
L=$(cd "$(dirname "$0")" && pwd)
cp "$L/prd050_probe_test.go" "$L/cilium/operator/pkg/model/translation/gateway-api/prd050_probe_test.go"
cd "$L/cilium" || exit 1
echo "cilium $(git describe --tags) $(git rev-parse HEAD), $(go version)"
PRD050_BOOTSTRAP="$L/cfg/gateway.json" go test -count=1 -run 'TestPRD050(Probe|Bootstrap)$' -v ./operator/pkg/model/translation/gateway-api/ > "$L/render.log" 2>&1
rc=$?
echo "render exit=$rc"
grep -v "^=== RUN" "$L/render.log"
exit $rc
```

### `up.sh`

```sh
#!/bin/sh
# Gateway and back ends on the pinned cilium-envoy, one rootless pod, published on 127.0.0.1 only.
L=$(cd "$(dirname "$0")" && pwd)
IMG=quay.io/cilium/cilium-envoy@sha256:ba0ab8adac082d50d525fd2c5ba096c8facea3a471561b7c61c7a5b9c2e0de0d
podman pod create --name prd050 -p 127.0.0.1:18443:443 > /dev/null
podman run -d --pod prd050 --name prd050-backend -v "$L/cfg:/cfg:ro" --entrypoint /usr/bin/cilium-envoy "$IMG" -c /cfg/backend.yaml --disable-hot-restart -l warning > /dev/null
podman run -d --pod prd050 --name prd050-gateway -v "$L/cfg:/cfg:ro" --entrypoint /usr/bin/cilium-envoy "$IMG" -c /cfg/gateway.json --disable-hot-restart -l info > /dev/null
i=0
while [ $i -lt 30 ]; do
  if podman logs prd050-gateway 2>&1 | grep -q "starting main dispatch loop"; then echo "gateway ready after ${i}s"; break; fi
  i=$((i+1)); sleep 1
done
podman run --rm --entrypoint /usr/bin/cilium-envoy "$IMG" --version 2>/dev/null | grep version
```

### `cases.sh`

```sh
#!/bin/sh
# PRD-050 client cases against the gateway on 127.0.0.1:18443 (see up.sh).
L=$(cd "$(dirname "$0")" && pwd)
D=$L/cfg
O=$L/out
mkdir -p "$O"
# case_run <id> <sni host> <none|good|wrong> [extra curl arguments]
case_run() {
  id=$1; host=$2; who=$3; shift 3
  : > "$O/$id.b"; : > "$O/$id.h"
  set -- --resolve "$host:18443:127.0.0.1" --cacert "$D/server-ca.crt" -sS --max-time 5 -D "$O/$id.h" -o "$O/$id.b" -w '%{http_code}' "$@"
  if [ "$who" != none ]; then set -- --cert "$D/tpp-$who.crt" --key "$D/tpp-$who.key" "$@"; fi
  code=$(curl "$@" "https://$host:18443/" 2>"$O/$id.e")
  rc=$?
  echo "$id sni=$host cert=$who curl_exit=$rc http=$code body=$(tr -d '\n' < "$O/$id.b")" \
    "$(grep -i '^x-seen\|^x-peer' "$O/$id.h" | tr -d '\r' | tr '\n' ' ')$(tr -d '\n' < "$O/$id.e")"
}
echo "== HTTPS listener *.org1 whose Gateway asks for client validation"
case_run C1 www.org1.example.test none
case_run C2 www.org1.example.test good
case_run C3 www.org1.example.test wrong
openssl s_client -connect 127.0.0.1:18443 -servername www.org1.example.test -CAfile "$D/server-ca.crt" </dev/null 2>/dev/null |
  grep -E "^subject=|client certificate CA names|Verify return code"
echo "== a certificate header set by the client, beside a control header"
case_run C4 www.org1.example.test none -H 'x-forwarded-client-cert: Hash=00;Subject="CN=forged"' -H 'x-probe: control-123'
echo "== TLS session for org1's listener, Host of another listener's route"
case_run C5 www.org1.example.test none -H 'Host: www.org2.example.test'
case_run C9 www.org1.example.test none -H 'Host: api.org1.example.test'
echo "== TLS passthrough listener api.org1; the back end terminates mutual TLS"
case_run C6 api.org1.example.test good
case_run C7 api.org1.example.test wrong
case_run C8 api.org1.example.test none
openssl s_client -connect 127.0.0.1:18443 -servername api.org1.example.test -CAfile "$D/server-ca.crt" \
  -cert "$D/tpp-good.crt" -key "$D/tpp-good.key" </dev/null 2>/dev/null |
  grep -E -A1 "^subject=|Acceptable client certificate CA names" | grep -v "^--"
```

### `down.sh`

```sh
#!/bin/sh
# Envoy log lines and error lines of both containers (Envoy logs to stderr), then remove the pod.
b=$(podman logs prd050-backend 2>&1); g=$(podman logs prd050-gateway 2>&1)
echo "envoy log lines: backend $(printf '%s\n' "$b" | grep -c '^\[') gateway $(printf '%s\n' "$g" | grep -c '^\[')"
echo "error lines: backend $(printf '%s\n' "$b" | grep -c -E '\[(error|critical)\]') gateway $(printf '%s\n' "$g" | grep -c -E '\[(error|critical)\]')"
podman pod rm -f prd050 > /dev/null
echo "prd050 containers left: $(podman ps -a --format '{{.Names}}' 2>/dev/null | grep -c prd050)"
```

### `all.sh`

```sh
#!/bin/sh
# One end-to-end lab run: fresh certificates, render, up, cases, down.
L=$(cd "$(dirname "$0")" && pwd)
sh "$L/certs.sh" || exit 1
sh "$L/render.sh" > "$L/render.out" 2>&1; echo "render exit=$?"
sh "$L/up.sh" 2>/dev/null
sh "$L/cases.sh" > "$L/cases.out" 2>&1; echo "cases exit=$?"
sh "$L/down.sh" 2>/dev/null
```

## Output

### `all.out`

```text
render exit=0
gateway ready after 0s
/usr/bin/cilium-envoy  version: 2437d2edeaf4d9b56ef279bd0d71127440c067aa/1.36.6/Distribution/RELEASE/BoringSSL
cases exit=0
envoy log lines: backend 1 gateway 88
error lines: backend 0 gateway 0
prd050 containers left: 0
```

### `render.out`

```text
cilium v1.19.3 f5eb641b18502ebeca412b40c1bec7ff3d8bf9eb, go version go1.25.0 linux/amd64
render exit=0
    prd050_probe_test.go:139: Q1 decode: listener console-https-org1 and its certificateRefs decoded; tls.frontendValidation is absent after decoding into the operator's Gateway type
    prd050_probe_test.go:171: ingestion: 4 HTTP listeners, 4 TLS passthrough listeners
    prd050_probe_test.go:184: service kube-system/cilium-gateway-console type=ClusterIP
    prd050_probe_test.go:234: Q3 chain sni=[] hcm=listener-insecure rds=listener-insecure forward_client_cert_details=SANITIZE set_current_client_cert_details=false
    prd050_probe_test.go:219: Q2 chain sni=[console.sov.example.test] require_client_certificate=false validation_context=false
    prd050_probe_test.go:234: Q3 chain sni=[console.sov.example.test] hcm=listener-secure rds=listener-secure forward_client_cert_details=SANITIZE set_current_client_cert_details=false
    prd050_probe_test.go:219: Q2 chain sni=[*.org1.example.test] require_client_certificate=false validation_context=false
    prd050_probe_test.go:234: Q3 chain sni=[*.org1.example.test] hcm=listener-secure rds=listener-secure forward_client_cert_details=SANITIZE set_current_client_cert_details=false
    prd050_probe_test.go:219: Q2 chain sni=[*.org2.example.test] require_client_certificate=false validation_context=false
    prd050_probe_test.go:234: Q3 chain sni=[*.org2.example.test] hcm=listener-secure rds=listener-secure forward_client_cert_details=SANITIZE set_current_client_cert_details=false
    prd050_probe_test.go:245: Q5 chain sni=[api.org1.example.test] transport_socket=false tcp_proxy cluster=org1:fingate-api:8443
    prd050_probe_test.go:265: Q4 route table listener-secure domains=www.org1.example.test,www.org1.example.test:*,www.org2.example.test,www.org2.example.test:*
--- PASS: TestPRD050Probe (0.01s)
    prd050_probe_test.go:139: Q1 decode: listener console-https-org1 and its certificateRefs decoded; tls.frontendValidation is absent after decoding into the operator's Gateway type
    prd050_probe_test.go:171: ingestion: 4 HTTP listeners, 4 TLS passthrough listeners
    prd050_probe_test.go:184: service kube-system/cilium-gateway-console type=ClusterIP
    prd050_probe_test.go:314: listener listener address=0.0.0.0:443 filter_chains=5
    prd050_probe_test.go:385: wrote <lab>/cfg/gateway.json: 1 listeners, 3 clusters, 3 secrets
--- PASS: TestPRD050Bootstrap (0.00s)
PASS
ok  	github.com/cilium/cilium/operator/pkg/model/translation/gateway-api	0.045s
```

### `cases.out`

```text
== HTTPS listener *.org1 whose Gateway asks for client validation
C1 sni=www.org1.example.test cert=none curl_exit=0 http=200 body=backend=org1-web 
C2 sni=www.org1.example.test cert=good curl_exit=0 http=200 body=backend=org1-web 
C3 sni=www.org1.example.test cert=wrong curl_exit=0 http=200 body=backend=org1-web 
subject=CN = *.org1.example.test
No client certificate CA names sent
Verify return code: 0 (ok)
== a certificate header set by the client, beside a control header
C4 sni=www.org1.example.test cert=none curl_exit=0 http=200 body=backend=org1-web x-seen-probe: control-123 
== TLS session for org1's listener, Host of another listener's route
C5 sni=www.org1.example.test cert=none curl_exit=0 http=200 body=backend=org2-web 
C9 sni=www.org1.example.test cert=none curl_exit=0 http=404 body= 
== TLS passthrough listener api.org1; the back end terminates mutual TLS
C6 sni=api.org1.example.test cert=good curl_exit=0 http=200 body=backend=fingate-api x-peer-subject: CN=tpp-good 
C7 sni=api.org1.example.test cert=wrong curl_exit=56 http=000 body= curl: (56) OpenSSL SSL_read: error:0A000418:SSL routines::tlsv1 alert unknown ca, errno 0
C8 sni=api.org1.example.test cert=none curl_exit=56 http=000 body= curl: (56) OpenSSL SSL_read: error:0A00045C:SSL routines::tlsv13 alert certificate required, errno 0
subject=CN = api.org1.example.test
issuer=CN = server-ca
Acceptable client certificate CA names
CN = org1-client-ca
```

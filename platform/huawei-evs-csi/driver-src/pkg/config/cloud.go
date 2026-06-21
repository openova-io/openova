package config

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/chnsz/golangsdk"
	"github.com/chnsz/golangsdk/openstack"

	"github.com/huaweicloud/huaweicloud-csi-driver/pkg/utils"
)

const (
	UserAgent = "huaweicloud-kubernetes-csi"
)

// CloudCredentials define
type CloudCredentials struct {
	Global struct {
		Cloud     string `gcfg:"cloud"`
		AuthURL   string `gcfg:"auth-url"`
		Region    string `gcfg:"region"`
		Insecure  bool   `gcfg:"insecure"`
		AccessKey string `gcfg:"access-key"`
		SecretKey string `gcfg:"secret-key"`
		ProjectID string `gcfg:"project-id"`
		Idc       bool   `gcfg:"idc"`
	}

	Vpc struct {
		ID              string `gcfg:"id"`
		SubnetID        string `gcfg:"subnet-id"`
		SecurityGroupID string `gcfg:"security-group-id"`
	}

	CloudClient *golangsdk.ProviderClient
}

type serviceCatalog struct {
	Name             string
	Version          string
	Scope            string
	Admin            bool
	ResourceBase     string
	WithOutProjectID bool
}

var allServiceCatalog = map[string]serviceCatalog{
	"ecs": {
		Name:    "ecs",
		Version: "v1",
	},
	"ecsV21": {
		Name:    "ecs",
		Version: "v2.1",
	},
	"evsV1": {
		Name:    "evs",
		Version: "v1",
	},
	"evsV2": {
		Name:    "evs",
		Version: "v2",
	},
	"evsV21": {
		Name:    "evs",
		Version: "v2.1",
	},
	"sfsV2": {
		Name:    "sfs",
		Version: "v2",
	},
	"sfsTurboV1": {
		Name:    "sfs-turbo",
		Version: "v1",
	},
}

func newServiceClient(cc *CloudCredentials, catalogName, region string) (*golangsdk.ServiceClient, error) {
	catalog, ok := allServiceCatalog[catalogName]
	if !ok {
		return nil, fmt.Errorf("service type %s is invalid or not supportted", catalogName)
	}

	client := cc.CloudClient
	// update ProjectID and region in ProviderClient
	clone := new(golangsdk.ProviderClient)
	*clone = *client
	clone.ProjectID = client.ProjectID
	clone.AKSKAuthOptions.ProjectId = client.ProjectID
	clone.AKSKAuthOptions.Region = region

	sc := &golangsdk.ServiceClient{
		ProviderClient: clone,
	}

	if catalog.Scope == "global" {
		sc.Endpoint = fmt.Sprintf("https://%s.%s/", catalog.Name, cc.Global.Cloud)
	} else {
		sc.Endpoint = fmt.Sprintf("https://%s.%s.%s/", catalog.Name, region, cc.Global.Cloud)
	}

	sc.ResourceBase = sc.Endpoint
	if catalog.Version != "" {
		sc.ResourceBase = sc.ResourceBase + catalog.Version + "/"
	}
	if !catalog.WithOutProjectID {
		sc.ResourceBase = sc.ResourceBase + client.ProjectID + "/"
	}
	if catalog.ResourceBase != "" {
		sc.ResourceBase = sc.ResourceBase + catalog.ResourceBase + "/"
	}

	return sc, nil
}

func (c *CloudCredentials) Validate() error {
	err := c.newCloudClient()
	if err != nil {
		return err
	}
	return nil
}

func (c *CloudCredentials) newCloudClient() error {
	ao := golangsdk.AKSKAuthOptions{
		IdentityEndpoint: c.Global.AuthURL,
		AccessKey:        c.Global.AccessKey,
		SecretKey:        c.Global.SecretKey,
		ProjectId:        c.Global.ProjectID,
		ProjectName:      c.Global.Region,
	}

	client, err := openstack.NewClient(ao.IdentityEndpoint)
	if err != nil {
		return err
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: c.Global.Insecure,
		},
	}

	client.HTTPClient = http.Client{
		Transport: &utils.LogRoundTripper{
			Rt: transport,
		},
	}

	// ── OpenOva IDC (no-Keystone) bypass — #3971 ──────────────────────────
	// On HCS deployments such as kom4dc / me-east-215 the IAM APIGW does NOT
	// publish the Keystone v3 catalog/token endpoints: GET /v3/auth/catalog
	// and POST /v3/auth/tokens both return APIGW.0101 "API does not exist".
	// The stock openstack.Authenticate() below performs exactly that catalog
	// fetch (v3AKSKAuth → catalog.List), so the driver CrashLoops at startup
	// before it ever issues an EVS call — even though the EVS data plane is
	// fully reachable with AK/SK request signing.
	//
	// This is the purpose the `idc` cloud-config flag was reserved for. When
	// it is set we DO NOT call Keystone at all. Instead we populate the
	// ProviderClient's AK/SK signing options + ProjectID directly. The
	// golangsdk per-request signer engages purely on
	// `AKSKAuthOptions.AccessKey != ""` (provider_client.go doRequest), and
	// newServiceClient() below computes every EVS/ECS endpoint locally as
	// https://<svc>.<region>.<cloud>/ (it never consults the Keystone
	// catalog), so no catalog lookup is needed for normal operation.
	//
	// A non-empty project-id is REQUIRED in idc mode: with no Keystone we
	// cannot resolve project-name → project-id, and every EVS resource path
	// is /<version>/<project-id>/cloudvolumes/... .
	//
	// Public Huawei Cloud (idc unset/false) keeps the stock Keystone flow
	// untouched.
	if c.Global.Idc {
		if c.Global.ProjectID == "" {
			return fmt.Errorf("idc mode requires a non-empty project-id in cloud-config " +
				"(no Keystone is available to resolve project-name → project-id)")
		}
		// Mirror v3AKSKAuth's post-conditions WITHOUT the catalog/token calls.
		client.AKSKAuthOptions = ao
		client.AKSKAuthOptions.DomainID = ""
		client.AKSKAuthOptions.Region = c.Global.Region
		client.ProjectID = c.Global.ProjectID
		client.AKSKAuthOptions.ProjectId = c.Global.ProjectID
		// No EndpointLocator is set: newServiceClient() derives endpoints
		// directly from cloud + region, so the catalog-backed locator is
		// never consulted.
		c.CloudClient = client
		c.CloudClient.UserAgent.Prepend(UserAgent)
		return nil
	}

	err = openstack.Authenticate(client, ao)
	if err != nil {
		return err
	}

	c.CloudClient = client
	c.CloudClient.UserAgent.Prepend(UserAgent)
	return nil
}

func (c *CloudCredentials) SFSTurboV1Client() (*golangsdk.ServiceClient, error) {
	return newServiceClient(c, "sfsTurboV1", c.Global.Region)
}

func (c *CloudCredentials) SFSV2Client() (*golangsdk.ServiceClient, error) {
	return newServiceClient(c, "sfsV2", c.Global.Region)
}

func (c *CloudCredentials) EcsV1Client() (*golangsdk.ServiceClient, error) {
	return newServiceClient(c, "ecs", c.Global.Region)
}

func (c *CloudCredentials) EcsV21Client() (*golangsdk.ServiceClient, error) {
	return newServiceClient(c, "ecsV21", c.Global.Region)
}

func (c *CloudCredentials) EvsV2Client() (*golangsdk.ServiceClient, error) {
	return newServiceClient(c, "evsV2", c.Global.Region)
}

func (c *CloudCredentials) EvsV21Client() (*golangsdk.ServiceClient, error) {
	return newServiceClient(c, "evsV21", c.Global.Region)
}

func (c *CloudCredentials) EvsV1Client() (*golangsdk.ServiceClient, error) {
	return newServiceClient(c, "evsV1", c.Global.Region)
}

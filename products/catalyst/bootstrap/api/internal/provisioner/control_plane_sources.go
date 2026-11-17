// Who may reach the Kubernetes API port 6443 of a new Sovereign (NET-011).
//
// The OpenTofu module opens port 6443 to the Sovereign's own addresses and to
// two lists that this file computes:
//
//	provisioner_cidrs  the source ranges of this API's own outbound traffic,
//	                   from CATALYST_EGRESS_CIDRS. The provisioner dials the
//	                   new cluster's API during phase 1; without these ranges
//	                   it cannot reach the cluster.
//	admin_cidrs        the ranges the sovereign-admin works from: the request
//	                   field adminCIDRs, or CATALYST_DEFAULT_ADMIN_CIDRS
//	                   when the request carries none.
//
// Both environment values are comma-separated lists. Every entry is an IPv4
// network in canonical CIDR form, such as 198.51.100.0/28; a prefix of length
// 0 is refused. A create request is refused with HTTP 422 when
// CATALYST_EGRESS_CIDRS is empty, or when an entry of either list is not such
// a network. The module's variables refuse the same values at `tofu plan`.

package provisioner

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"strings"
)

// The environment variables of the two lists.
const (
	EnvEgressCIDRs       = "CATALYST_EGRESS_CIDRS"
	EnvDefaultAdminCIDRs = "CATALYST_DEFAULT_ADMIN_CIDRS"
)

// ErrEgressCIDRsUnset is the refusal of a create request while
// CATALYST_EGRESS_CIDRS holds no entry.
var ErrEgressCIDRsUnset = errors.New("CATALYST_EGRESS_CIDRS is not set: the provisioner could not reach the new cluster")

// cidrList splits a comma-separated value into its trimmed, non-empty
// entries. It never returns nil, so the tfvars file holds [] and not null.
func cidrList(raw string) []string {
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		if entry := strings.TrimSpace(part); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// EgressCIDRs returns the entries of CATALYST_EGRESS_CIDRS; never nil.
func EgressCIDRs() []string {
	return cidrList(os.Getenv(EnvEgressCIDRs))
}

// AdminCIDRs returns the request's adminCIDRs, or the entries of
// CATALYST_DEFAULT_ADMIN_CIDRS when the request has none; never nil.
func AdminCIDRs(req Request) []string {
	if len(req.AdminCIDRs) > 0 {
		return append([]string{}, req.AdminCIDRs...)
	}
	return cidrList(os.Getenv(EnvDefaultAdminCIDRs))
}

// checkSourceCIDR returns an error unless entry is an IPv4 network in
// canonical CIDR form with a prefix longer than 0. list names where the
// entry came from.
func checkSourceCIDR(list, entry string) error {
	prefix, err := netip.ParsePrefix(entry)
	if err != nil || !prefix.Addr().Is4() || prefix.Masked() != prefix || prefix.String() != entry {
		return fmt.Errorf("%s entry %q is not an IPv4 network in CIDR form, such as 198.51.100.0/28", list, entry)
	}
	if prefix.Bits() == 0 {
		return fmt.Errorf("%s entry %q is refused: the API port is never open to every address", list, entry)
	}
	return nil
}

// CheckControlPlaneSources returns the reason a create request must be
// refused with HTTP 422, or nil.
func CheckControlPlaneSources(req Request) error {
	egress := EgressCIDRs()
	if len(egress) == 0 {
		return ErrEgressCIDRsUnset
	}
	for _, entry := range egress {
		if err := checkSourceCIDR(EnvEgressCIDRs, entry); err != nil {
			return err
		}
	}
	list := "adminCIDRs"
	if len(req.AdminCIDRs) == 0 {
		list = EnvDefaultAdminCIDRs
	}
	for _, entry := range AdminCIDRs(req) {
		if err := checkSourceCIDR(list, entry); err != nil {
			return err
		}
	}
	return nil
}

// AdmitRequest judges a create request. An error of Validate() is answered
// with HTTP 400, as before; an error of CheckControlPlaneSources with HTTP
// 422. Without an error the status is 200 and means nothing.
func AdmitRequest(req *Request) (int, error) {
	if err := req.Validate(); err != nil {
		return http.StatusBadRequest, err
	}
	if err := CheckControlPlaneSources(*req); err != nil {
		return http.StatusUnprocessableEntity, err
	}
	return http.StatusOK, nil
}

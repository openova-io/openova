package provisioner

// The objects a cloud adapter creates before the plan (CLD-G12, 2026-11-21).
//
// What the engine changes in the cloud for an install is in that install's
// state: an object that an adapter makes outside OpenTofu's own allocation
// (providers.PreflightResult.Imports) is imported into the state at its
// resource address before `tofu plan`, so the plan uses it, every resource
// derived from it is planned from the same value, a second plan shows no
// change, and the wipe's `tofu destroy` removes it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// stateFile is the state of a working directory: the modules use the local
// backend.
const stateFile = "terraform.tfstate"

// stateAddresses returns the resource instance addresses of the managed
// resources that the state of deployDir holds: none when there is no state
// yet. A state that cannot be read is an error, so that no object is made
// for an address the state may already own.
func stateAddresses(deployDir string) (map[string]bool, error) {
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(deployDir, stateFile)))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the state: %w", err)
	}
	var st struct {
		Resources []struct {
			Module    string `json:"module"`
			Mode      string `json:"mode"`
			Type      string `json:"type"`
			Name      string `json:"name"`
			Instances []struct {
				IndexKey any `json:"index_key"`
			} `json:"instances"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("decode the state: %w", err)
	}
	out := map[string]bool{}
	for _, r := range st.Resources {
		if r.Mode != "managed" {
			continue
		}
		base := r.Type + "." + r.Name
		if r.Module != "" {
			base = r.Module + "." + base
		}
		for _, in := range r.Instances {
			switch k := in.IndexKey.(type) {
			case nil:
				out[base] = true
			case string:
				out[base+"["+strconv.Quote(k)+"]"] = true
			case float64:
				out[base+"["+strconv.FormatFloat(k, 'f', -1, 64)+"]"] = true
			}
		}
	}
	return out, nil
}

// importPreCreated imports every object of imports whose address the state
// does not hold (inState, read before the adapter ran) with `tofu import`, and
// returns the objects the state now owns. A failed import fails the prov: the
// object stays in the cloud under the deployment's name prefix, which the
// wipe's purge finds.
func (p *Provisioner) importPreCreated(ctx context.Context, deployDir string, imports []providers.StateImport, inState map[string]bool, emit func(string, string, string)) ([]providers.StateImport, error) {
	var owned []providers.StateImport
	for _, im := range imports {
		if im.Address == "" || im.ID == "" {
			return nil, fmt.Errorf("the cloud adapter returned an import without an address or an id: %+v", im)
		}
		if inState[im.Address] {
			emit("tofu-import", "warn", fmt.Sprintf("%s is already in the state; %s (%s) is not imported", im.Address, im.Label, im.ID))
			continue
		}
		emit("tofu-import", "info", fmt.Sprintf("Importing %s (%s, %s), which the cloud adapter created before the plan", im.Address, im.Label, im.ID))
		if err := p.runTofu(ctx, deployDir, []string{"import", "-input=false", "-no-color", im.Address, im.ID}, emit); err != nil {
			return nil, fmt.Errorf("tofu import %s %s: %w", im.Address, im.ID, err)
		}
		owned = append(owned, im)
	}
	return owned, nil
}

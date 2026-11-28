package main

import (
	_ "embed"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/core/controllers/pkg/upstream"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/lifecycle"
)

// settableKitKeysText is a byte copy of docs/contracts/c5-kit-keys-settable.txt,
// because an embed cannot reach outside this Go module. TestSettableKitKeysCopy
// fails when the two differ; change both in one pull request.
//
//go:embed contract/c5-kit-keys-settable.txt
var settableKitKeysText string

// settableKitKeys parses a settable key list and refuses a key that is not
// one of kit, the kit keys of contract C5 (upstream.KitKeys).
func settableKitKeys(text string, kit []string) (map[string]bool, error) {
	keys, err := lifecycle.ParseKitKeyList(text)
	for k := range keys {
		if !slices.Contains(kit, k) {
			return nil, fmt.Errorf("%s is not a kit key of docs/contracts/c5-kit-keys.txt", k)
		}
	}
	return keys, err
}

// newKitKeysRoute builds PUT /api/v1/sovereign/kit-keys/{key} with the commit
// library of package intent (intentGitConfig). Without that configuration the
// route has no Git and answers 503; with a settable list that is refused it
// has no settable key and refuses every key. getenv is os.Getenv in production.
func newKitKeysRoute(log *slog.Logger, getenv func(string) string) *lifecycle.KitKeysRoute {
	route := &lifecycle.KitKeysRoute{Log: log, FQDN: strings.TrimSpace(getenv("SOVEREIGN_FQDN"))}
	var err error
	if route.Settable, err = settableKitKeys(settableKitKeysText, upstream.KitKeys()); err != nil {
		log.Error("kit keys: the settable list is refused; no kit key can be set", "err", err)
	}
	cfg, err := intentGitConfig(getenv)
	if err == nil {
		route.Git, err = gitwrite.New(cfg)
	}
	if err != nil {
		route.Git = nil
		log.Warn("kit keys: the commit library is not configured; the route answers 503", "err", err)
	}
	return route
}

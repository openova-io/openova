package instances

import (
	"fmt"
	"net/http"
)

// Refusal is an Application create refused because of what its Blueprint is
// (contract C1: spec.class and spec.lifecycle), before anything is written.
type Refusal struct {
	Status  int
	Code    string
	Message string
}

// ClassLifecycleRefusal returns the refusal of a create of an Application of
// the Blueprint named blueprint, or nil when the create may go on:
//
//   - class platform-component: only a sovereign-admin installs it
//     (403 class-not-installable);
//   - lifecycle deprecated: no new Application (409 blueprint-deprecated);
//     the Applications that exist keep running.
//
// An empty class or lifecycle (an object written before contract C1) is not
// refused: the definition's defaults are application and experimental.
func ClassLifecycleRefusal(blueprint, class, lifecycle string, sovereignAdmin bool) *Refusal {
	switch {
	case class == "platform-component" && !sovereignAdmin:
		return &Refusal{Status: http.StatusForbidden, Code: "class-not-installable",
			Message: fmt.Sprintf("Blueprint %s is a platform component; only a sovereign-admin installs it", blueprint)}
	case lifecycle == "deprecated":
		return &Refusal{Status: http.StatusConflict, Code: "blueprint-deprecated",
			Message: fmt.Sprintf("Blueprint %s is deprecated; no new Application may be created from it", blueprint)}
	}
	return nil
}

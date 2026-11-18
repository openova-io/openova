package sovereignobject

import "time"

// ConditionType is the type of the condition that states the region separation.
const ConditionType = "RegionSeparation"

// now is the clock of lastTransitionTime; a test replaces it.
var now = time.Now

// Separation is the region separation of a registry document, by the rule of
// the provider modules: site when every region has its own cloudRegion; zone
// when the cloud regions repeat but every region has its own pair of
// cloudRegion and az; none otherwise, and always for fewer than two regions.
func Separation(network map[string]interface{}) string {
	regions, _ := network["regions"].([]interface{})
	clouds, pairs := map[string]bool{}, map[string]bool{}
	for _, r := range regions {
		m, _ := r.(map[string]interface{})
		cloud, _ := m["cloudRegion"].(string)
		az, _ := m["az"].(string)
		clouds[cloud] = true
		pairs[cloud+"/"+az] = true
	}
	switch n := len(regions); {
	case n < 2:
		return "none"
	case len(clouds) == n:
		return "site"
	case len(pairs) == n:
		return "zone"
	}
	return "none"
}

// separationCondition is the condition RegionSeparation for sep. It keeps the
// lastTransitionTime of the same condition of current while its status stays.
func separationCondition(sep string, regions int, current map[string]interface{}) map[string]interface{} {
	status, reason := "False", "SameFailureDomain"
	message := "Regions share one failure domain. Not a disaster-recovery configuration."
	switch {
	case sep == "zone":
		status, reason, message = "True", "SeparateZones", "Every region runs in its own availability zone."
	case sep == "site":
		status, reason, message = "True", "SeparateSites", "Every region runs in its own cloud region."
	case regions < 2:
		message = "The Sovereign has one region: one failure domain."
	}
	since := now().UTC().Format(time.RFC3339)
	st, _ := current["status"].(map[string]interface{})
	list, _ := st["conditions"].([]interface{})
	for _, x := range list {
		old, _ := x.(map[string]interface{})
		if t, _ := old["lastTransitionTime"].(string); old["type"] == ConditionType && old["status"] == status && t != "" {
			since = t
		}
	}
	return map[string]interface{}{"type": ConditionType, "status": status, "reason": reason, "message": message, "lastTransitionTime": since}
}

package manager

import "github.com/pax-beehive/pax-manager/internal/manager/runtimeconfig"

// Tests written before feature gates existed exercise Secretary, Notifications,
// and Collaboration as ordinary users. They run with every feature at ga.
// Gate tests build stores from runtimeconfig.DefaultRegistry explicitly.
func init() {
	runtimeConfigRegistry = openFeatureRegistry
}

func openFeatureRegistry() []runtimeconfig.Entry {
	entries := runtimeconfig.DefaultRegistry()
	for i := range entries {
		if entries[i].Kind == runtimeconfig.KindFeature {
			entries[i].Default = runtimeconfig.Feature{Stage: runtimeconfig.StageGA}
		}
	}
	return entries
}

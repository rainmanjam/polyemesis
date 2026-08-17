package oauth

import "testing"

func TestZZProbeCapabilityVsCode(t *testing.T) {
	for _, row := range PlatformCapabilities() {
		_, sso := Providers()[row.Platform]
		_, tgt := TargetsFor(row.Platform)
		_, mk := ManualKeyFor(row.Platform)
		_, md := MetadataFor(row.Platform)
		_, lc := LifecycleFor(row.Platform)
		t.Logf("%-10s sso:%-5v/%-8s key:%-5v/%-8s meta:%-5v/%-8s life:%-5v/%-8s",
			row.PresetID,
			sso, row.Caps[CapSSO],
			tgt || mk, row.Caps[CapStreamKey],
			md, row.Caps[CapMetadata],
			lc, row.Caps[CapBroadcastLifecycle])
	}
}

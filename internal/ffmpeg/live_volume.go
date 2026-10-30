package ffmpeg

import (
	"regexp"
	"strconv"
)

// liveVolumeRE matches one named volume stage of a destination graph, the form
// routing emits as volume@<name>=volume=<level>. It is the only place that
// spelling is recognised on this side of the contract.
var liveVolumeRE = regexp.MustCompile(`volume@([A-Za-z0-9_]+)=volume=([0-9.]+)`)

// MaskLiveVolumes blanks the level of every named volume stage in a filter
// graph and returns the rest unchanged. A destination's restart signature is
// taken over the masked graph, so a change of level alone does not restart it
// and is applied live instead. A change of anything else still restarts.
func MaskLiveVolumes(graph string) string {
	return liveVolumeRE.ReplaceAllString(graph, "volume@$1=volume=*")
}

// LiveVolumeLevels returns instance name (without the volume@ prefix) to level
// for every named volume stage in a graph. A level that does not parse is
// left out rather than guessed at.
func LiveVolumeLevels(graph string) map[string]float64 {
	out := map[string]float64{}
	for _, m := range liveVolumeRE.FindAllStringSubmatch(graph, -1) {
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		out[m[1]] = v
	}
	return out
}

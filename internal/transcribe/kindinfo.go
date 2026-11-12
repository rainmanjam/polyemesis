package transcribe

import "github.com/rainmanjam/polyemesis/internal/jobs"

// Info is the jobs-page entry for the transcription kind.
var Info = jobs.KindInfo{
	Kind:        KindTranscribe,
	Label:       "Transcription",
	Description: "Speech to text, one audio track at a time. Because each microphone is on its own track, this gives speaker attribution without a diarization model.",
}

// Limit is 1: whisper saturates every core it is given, so a second
// transcription buys nothing and costs the first one half the machine.
const Limit = 1

// Register wires the processor into a queue under KindTranscribe.
func (p *Processor) Register(r jobs.Registerer) error {
	return r.Register(KindTranscribe, Limit, p)
}

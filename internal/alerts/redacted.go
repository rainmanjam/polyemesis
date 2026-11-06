package alerts

import "github.com/rainmanjam/polyemesis/internal/redact"

// Redacted returns a copy of the event with nothing secret left in it.
//
// Applied by Publish rather than left to callers: the engine builds these
// events in a dozen places and exactly one of them has to be careless for a
// stream key to end up in a chat room that outlives the broadcast.
func (e Event) Redacted() Event {
	e.Title = redact.Redact(e.Title)
	e.Text = redact.Redact(e.Text)
	if len(e.Fields) == 0 {
		return e
	}
	fields := make([]Field, len(e.Fields))
	for i, f := range e.Fields {
		if redact.SecretName(f.Name) {
			fields[i] = Field{Name: f.Name, Value: redact.Mask}
			continue
		}
		fields[i] = Field{Name: f.Name, Value: redact.Redact(f.Value)}
	}
	e.Fields = fields
	return e
}

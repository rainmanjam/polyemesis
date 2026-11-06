package jobs

// KindInfo is what the jobs page shows beside a kind's switch. Each processor
// package exports its own next to the code that registers the kind, so the
// string, the label and the registration cannot drift apart.
type KindInfo struct {
	Kind        Kind
	Label       string
	Description string
}

// Registrar is the part of a Queue a processor needs in order to register.
type Registrar interface {
	Register(kind Kind, limit int, w Worker) error
}

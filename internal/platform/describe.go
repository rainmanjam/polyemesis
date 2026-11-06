package platform

// Info is one platform as a client reads it: the facts a console or a scripted
// client needs to name, list and gate a platform without carrying its own
// table. It is a SEPARATE TYPE from Row on purpose. Row is the server's
// working shape (it has no JSON tags and carries list-order fields that are
// internal to the API's own responses); Info is the published one, so a field
// reaches the wire only because somebody added it here.
type Info struct {
	// Key is the platform's id in the registry and the id of its primary preset.
	Key string `json:"key"`
	// Enum is the value a destination's platform column stores; absent for the
	// presets that save as custom (Instagram, LinkedIn, Dailymotion, Owncast).
	Enum string `json:"enum,omitempty"`
	// Name is the full display name ("YouTube Live"); ShortName is the one that
	// reads in a sentence or beside a chat line ("YouTube").
	Name      string   `json:"name"`
	ShortName string   `json:"shortName"`
	Presets   []string `json:"presets"`
	// Destination reports whether a stored destination may carry Enum.
	Destination bool `json:"destination"`
	// Retired reports that the platform shut down: it still needs a label for
	// rows saved before, and nothing offers it any more.
	Retired bool `json:"retired"`
	// Chat reports that polyemesis has a chat adapter for the platform, so a
	// message can carry Enum. Vimeo, TikTok and the preset-only rows have none.
	Chat bool `json:"chat"`
	// ChatSendMax is the longest chat message polyemesis may send, in runes;
	// zero when it cannot send.
	ChatSendMax int `json:"chatSendMax"`
}

// Rules are the platform rules that are not per-platform rows but that a client
// applies to what an operator types.
type Rules struct {
	// IVSHostSuffix is the IVS contribute domain, leading dot included; IVSApp is
	// the one application an IVS ingest accepts.
	IVSHostSuffix string `json:"ivsHostSuffix"`
	IVSApp        string `json:"ivsApp"`
	// XGoLiveTitleMax is the longest X go-live title, in characters.
	XGoLiveTitleMax int `json:"xGoLiveTitleMax"`
}

// Directory is the whole read-only description of the registry that is served
// at GET /api/v1/platforms/registry and committed beside the console as its
// generated contract.
type Directory struct {
	Platforms []Info `json:"platforms"`
	Rules     Rules  `json:"rules"`
}

// Describe is the registry as clients read it, in registry order.
func Describe() Directory {
	d := Directory{
		Platforms: make([]Info, 0, len(rows)),
		Rules: Rules{
			IVSHostSuffix:   IVSIngestHostSuffix,
			IVSApp:          IVSApp,
			XGoLiveTitleMax: XGoLiveTitleMax,
		},
	}
	for _, r := range rows {
		d.Platforms = append(d.Platforms, Info{
			Key:         r.Key,
			Enum:        r.Enum,
			Name:        r.Name,
			ShortName:   r.ShortName,
			Presets:     append([]string{}, r.Presets...),
			Destination: r.AcceptsDestinations,
			Retired:     r.Retired != "",
			// Moderation is declared for exactly the platforms with a chat adapter
			// (Rumble's row says so with every action false);
			// TestRegistryAgreesWithTheChatAdapters holds that to internal/chat.
			Chat:        r.Moderation != nil,
			ChatSendMax: r.Chat.SendMax,
		})
	}
	return d
}

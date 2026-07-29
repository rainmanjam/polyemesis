package mqtt

import (
	"context"
	"encoding/json"
)

// Home Assistant discovery.
//
// This publishes the device-level payload rather than one topic per component.
// Home Assistant's documentation presents the two as alternatives and states no
// preference between them -- the choice here is that a device payload keeps
// every entity attached to one polyemesis instance, so removing the instance
// removes the lot, and a per-component tree leaves orphans behind on exactly the
// operation where orphans hurt most.
//
// Entity ids are built from the same Slug that builds the topics, so an entity
// and the topic feeding it can never disagree about which thing they describe.

// haDevice identifies the install. Home Assistant groups every entity sharing
// these identifiers under one device card.
type haDevice struct {
	Identifiers  []string `json:"ids"`
	Name         string   `json:"name"`
	Manufacturer string   `json:"mf"`
	Model        string   `json:"mdl"`
	SoftwareVer  string   `json:"sw,omitempty"`
}

// haOrigin is the "added by" attribution Home Assistant shows on the device.
type haOrigin struct {
	Name        string `json:"name"`
	SoftwareVer string `json:"sw,omitempty"`
}

// haComponent is one entity.
//
// The field names are Home Assistant's own abbreviations. Spelled out they
// would be clearer to read here and wrong on the wire, so the long names are in
// the comments instead.
type haComponent struct {
	Platform      string `json:"p"`                 // binary_sensor | sensor
	Name          string `json:"name"`              //
	UniqueID      string `json:"uniq_id"`           // unique_id
	StateTopic    string `json:"stat_t"`            // state_topic
	ValueTemplate string `json:"val_tpl"`           // value_template
	DeviceClass   string `json:"dev_cla,omitempty"` // device_class
	UnitOfMeas    string `json:"unit_of_meas,omitempty"`
	StateClass    string `json:"stat_cla,omitempty"`
	PayloadOn     string `json:"pl_on,omitempty"`
	PayloadOff    string `json:"pl_off,omitempty"`
	EntityCat     string `json:"ent_cat,omitempty"` // entity_category
	Icon          string `json:"ic,omitempty"`
}

// haDiscovery is the whole device payload.
type haDiscovery struct {
	Device    haDevice               `json:"dev"`
	Origin    haOrigin               `json:"o"`
	Component map[string]haComponent `json:"cmps"`
	// Availability is what makes every entity go "unavailable" together when
	// polyemesis stops, driven by the same retained status topic the will
	// message writes. Without it a dead instance's entities keep showing their
	// last reading indefinitely, which is worse than showing nothing: a
	// dashboard that is confidently wrong.
	AvailabilityTopic string `json:"avty_t"`
	PayloadAvailable  string `json:"pl_avail"`
	PayloadNotAvail   string `json:"pl_not_avail"`
	QoS               byte   `json:"qos"`
}

// PublishDiscovery sends the Home Assistant device payload describing every
// source and destination in the snapshot.
//
// Retained, like everything else here: Home Assistant reads discovery topics
// when it starts, and a non-retained payload would only ever be seen by an
// instance that happened to be running at the moment polyemesis connected.
func (t *Telemetry) PublishDiscovery(ctx context.Context, snap Snapshot) error {
	inst := t.topics.Instance()
	d := haDiscovery{
		Device: haDevice{
			Identifiers:  []string{"polyemesis_" + inst},
			Name:         "polyemesis " + inst,
			Manufacturer: "polyemesis",
			Model:        "restreamer",
			SoftwareVer:  snap.Host.Version,
		},
		Origin:            haOrigin{Name: "polyemesis", SoftwareVer: snap.Host.Version},
		Component:         map[string]haComponent{},
		AvailabilityTopic: t.topics.Status(),
		PayloadAvailable:  Online,
		PayloadNotAvail:   Offline,
		QoS:               QoS,
	}

	add := func(key string, c haComponent) {
		c.UniqueID = "polyemesis_" + inst + "_" + key
		d.Component[key] = c
	}

	add("sources_live", haComponent{
		Platform:      "sensor",
		Name:          "Sources live",
		StateTopic:    t.topics.State(),
		ValueTemplate: "{{ value_json.sourcesLive }}",
		StateClass:    "measurement",
		Icon:          "mdi:video-input-component",
	})
	add("destinations_up", haComponent{
		Platform:      "sensor",
		Name:          "Destinations up",
		StateTopic:    t.topics.State(),
		ValueTemplate: "{{ value_json.destinationsUp }}",
		StateClass:    "measurement",
		Icon:          "mdi:broadcast",
	})

	for _, s := range snap.Sources {
		src := s.State.Slug
		topic := t.topics.Source(src)
		add(src+"_live", haComponent{
			Platform:      "binary_sensor",
			Name:          s.State.Name + " live",
			StateTopic:    topic,
			ValueTemplate: "{{ 'ON' if value_json.live else 'OFF' }}",
			DeviceClass:   "running",
			PayloadOn:     "ON",
			PayloadOff:    "OFF",
		})
		add(src+"_bitrate", haComponent{
			Platform:      "sensor",
			Name:          s.State.Name + " bitrate",
			StateTopic:    topic,
			ValueTemplate: "{{ value_json.bitrateKbps }}",
			UnitOfMeas:    "kbit/s",
			StateClass:    "measurement",
			Icon:          "mdi:speedometer",
		})

		for _, dest := range s.Dests {
			add(src+"_"+dest.Slug+"_running", haComponent{
				Platform:      "binary_sensor",
				Name:          dest.Name,
				StateTopic:    t.topics.Dest(src, dest.Slug),
				ValueTemplate: "{{ 'ON' if value_json.running else 'OFF' }}",
				DeviceClass:   "running",
				PayloadOn:     "ON",
				PayloadOff:    "OFF",
			})
		}
	}

	body, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return t.pub.Publish(ctx, t.topics.Discovery(), QoS, true, body)
}

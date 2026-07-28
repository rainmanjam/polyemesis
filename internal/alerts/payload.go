package alerts

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Payload limits taken from the two services' documented maxima. Exceeding any
// of them is a 400, and a 400 is not retried, so a message that was too long
// would simply never arrive.
const (
	discordMaxEmbeds     = 10
	discordMaxTitle      = 256
	discordMaxDesc       = 4096
	discordMaxFieldName  = 256
	discordMaxFieldValue = 1024
	slackMaxAttachments  = 20
	slackMaxText         = 3000
)

// Discord embed colours, chosen to match its own status palette so a critical
// alert reads as one at a glance.
const (
	colorInfo     = 0x3BA55D
	colorWarning  = 0xFAA61A
	colorCritical = 0xED4245
)

func embedColor(s Severity) int {
	switch s {
	case SeverityCritical:
		return colorCritical
	case SeverityWarning:
		return colorWarning
	default:
		return colorInfo
	}
}

// slackColor is the attachment bar, in Slack's hex form.
func slackColor(s Severity) string {
	return fmt.Sprintf("#%06X", embedColor(s))
}

// Encode renders a delivery in its rule's format.
//
// Every string that reaches here has already been through Redacted; the
// encoders add no data of their own beyond the rule NAME, never its URL.
func Encode(d Delivery) (body []byte, contentType string, err error) {
	switch d.Rule.Format {
	case FormatDiscord:
		body, err = encodeDiscord(d)
	case FormatSlack:
		body, err = encodeSlack(d)
	case FormatJSON, "":
		body, err = encodeJSON(d)
	default:
		return nil, "", fmt.Errorf("unknown alert format %q", d.Rule.Format)
	}
	return body, "application/json", err
}

// jsonAlert is one item in the generic payload. Fields are an object rather
// than a list because the consumer of a generic webhook is a script, and a
// script wants to index by name.
type jsonAlert struct {
	Type     Type              `json:"type"`
	Severity Severity          `json:"severity"`
	Key      string            `json:"key"`
	Title    string            `json:"title"`
	Text     string            `json:"text,omitempty"`
	Count    int               `json:"count"`
	FirstAt  time.Time         `json:"firstAt"`
	LastAt   time.Time         `json:"lastAt"`
	Fields   map[string]string `json:"fields,omitempty"`
}

type jsonPayload struct {
	Source   string      `json:"source"`
	Rule     string      `json:"rule"`
	SentAt   time.Time   `json:"sentAt"`
	Overflow int         `json:"overflow,omitempty"`
	Alerts   []jsonAlert `json:"alerts"`
}

func encodeJSON(d Delivery) ([]byte, error) {
	p := jsonPayload{
		Source:   "polyemesis",
		Rule:     d.Rule.Name,
		SentAt:   deliveryTime(d),
		Overflow: d.Overflow,
		Alerts:   make([]jsonAlert, 0, len(d.Items)),
	}
	for _, it := range d.Items {
		a := jsonAlert{
			Type: it.Event.Type, Severity: it.Event.Severity, Key: it.Event.Key,
			Title: it.Event.Title, Text: it.Event.Text,
			Count: it.Count, FirstAt: it.First, LastAt: it.Last,
		}
		if len(it.Event.Fields) > 0 {
			a.Fields = make(map[string]string, len(it.Event.Fields))
			for _, f := range it.Event.Fields {
				a.Fields[f.Name] = f.Value
			}
		}
		p.Alerts = append(p.Alerts, a)
	}
	return json.Marshal(p)
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordEmbed struct {
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Color       int            `json:"color"`
	Timestamp   string         `json:"timestamp"`
	Fields      []discordField `json:"fields,omitempty"`
	Footer      *discordFooter `json:"footer,omitempty"`
}

type discordFooter struct {
	Text string `json:"text"`
}

type discordPayload struct {
	Username string         `json:"username"`
	Content  string         `json:"content,omitempty"`
	Embeds   []discordEmbed `json:"embeds"`
}

func encodeDiscord(d Delivery) ([]byte, error) {
	p := discordPayload{Username: "polyemesis"}
	overflow := d.Overflow
	for i, it := range d.Items {
		if i >= discordMaxEmbeds {
			overflow += len(d.Items) - i
			break
		}
		e := discordEmbed{
			Title:       trunc(titleWithCount(it), discordMaxTitle),
			Description: trunc(it.Event.Text, discordMaxDesc),
			Color:       embedColor(it.Event.Severity),
			Timestamp:   it.Last.UTC().Format(time.RFC3339),
		}
		for _, f := range it.Event.Fields {
			e.Fields = append(e.Fields, discordField{
				Name:   trunc(f.Name, discordMaxFieldName),
				Value:  trunc(f.Value, discordMaxFieldValue),
				Inline: true,
			})
		}
		e.Footer = &discordFooter{Text: trunc(d.Rule.Name, discordMaxFieldValue)}
		p.Embeds = append(p.Embeds, e)
	}
	if overflow > 0 {
		p.Content = fmt.Sprintf("and %d more alert(s) not shown", overflow)
	}
	return json.Marshal(p)
}

type slackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

type slackAttachment struct {
	Color    string       `json:"color"`
	Fallback string       `json:"fallback"`
	Title    string       `json:"title"`
	Text     string       `json:"text,omitempty"`
	Fields   []slackField `json:"fields,omitempty"`
	Footer   string       `json:"footer,omitempty"`
	TS       int64        `json:"ts"`
}

type slackPayload struct {
	Text        string            `json:"text"`
	Attachments []slackAttachment `json:"attachments"`
}

func encodeSlack(d Delivery) ([]byte, error) {
	p := slackPayload{Text: slackSummary(d)}
	overflow := d.Overflow
	for i, it := range d.Items {
		if i >= slackMaxAttachments {
			overflow += len(d.Items) - i
			break
		}
		title := titleWithCount(it)
		a := slackAttachment{
			Color:    slackColor(it.Event.Severity),
			Fallback: trunc(title, slackMaxText),
			Title:    trunc(title, slackMaxText),
			Text:     trunc(it.Event.Text, slackMaxText),
			Footer:   d.Rule.Name,
			TS:       it.Last.Unix(),
		}
		for _, f := range it.Event.Fields {
			a.Fields = append(a.Fields, slackField{
				Title: trunc(f.Name, slackMaxText),
				Value: trunc(f.Value, slackMaxText),
				Short: true,
			})
		}
		p.Attachments = append(p.Attachments, a)
	}
	if overflow > 0 {
		p.Text += fmt.Sprintf(" (and %d more not shown)", overflow)
	}
	return json.Marshal(p)
}

// slackSummary is the notification line, which is all a phone shows.
func slackSummary(d Delivery) string {
	if len(d.Items) == 1 {
		return "polyemesis: " + trunc(titleWithCount(d.Items[0]), slackMaxText)
	}
	return fmt.Sprintf("polyemesis: %d alerts", len(d.Items)+d.Overflow)
}

// titleWithCount says how many times something happened, because "destination
// down" and "destination down, 40 times in the last minute" are different
// problems.
func titleWithCount(it Item) string {
	if it.Count > 1 {
		return fmt.Sprintf("%s (x%d)", it.Event.Title, it.Count)
	}
	return it.Event.Title
}

// deliveryTime is the newest event in the batch rather than time.Now, so an
// encoded payload is a pure function of its delivery and can be compared in a
// test.
func deliveryTime(d Delivery) time.Time {
	var newest time.Time
	for _, it := range d.Items {
		if it.Last.After(newest) {
			newest = it.Last
		}
	}
	return newest
}

// trunc cuts on a rune boundary and marks the cut, so a long FFmpeg error does
// not turn into a 400 from the far end.
func trunc(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max - 3
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " ") + "..."
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

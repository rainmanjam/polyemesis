package automod

import "strings"

// The closed set of reasons a moderation decision is allowed to give.
//
// This exists because of #495. The model checker used to put its own prose in
// Finding.Reason, internal/chat handed that straight to Hub.Ban, and the Twitch
// and Kick adapters POST it as the ban `reason` under the broadcaster's
// credential. A moderation record on a third-party platform is PERMANENT and is
// attributed to the broadcaster, so a viewer who could steer the model's prose
// -- which is the whole of prompt injection, and the model has just been handed
// their message as input -- could write text of their choosing onto it. The
// operator's own Instruction could come back out the same field.
//
// A prompt asking the model to behave is not a device; it is a request to a
// component with a non-zero error rate. The device is that the model CHOOSES
// FROM A LIST and the parse rejects anything else, so the only strings that can
// ever leave for a platform are the constant sentences below. Nothing a model
// emits is a value here -- it is at most a lookup key, and an invalid key is
// not a decision at all.

// Category is one reason, from a fixed set.
//
// A string rather than an iota int on purpose, matching Action and Checker: it
// crosses a JSON boundary, and an integer that silently renumbers when someone
// inserts a constant is the trap this package already avoids for those two.
type Category string

const (
	// CategoryHarassment is targeted abuse of a person.
	CategoryHarassment Category = "harassment"
	// CategoryHateSpeech is abuse aimed at a protected characteristic.
	CategoryHateSpeech Category = "hate_speech"
	// CategoryThreat is violence, threatened or encouraged.
	CategoryThreat Category = "threat"
	// CategorySexualContent is unwanted sexual content.
	CategorySexualContent Category = "sexual_content"
	// CategorySpam is advertising, scams and link flooding.
	CategorySpam Category = "spam"
	// CategoryOther is the model's escape hatch: still one of the enumerated
	// values, so it cannot become a way to smuggle prose through. Offered on
	// purpose -- without it, a model with an opinion that fits nothing above
	// has to pick a wrong label or emit an invalid one, and both are worse.
	CategoryOther Category = "other"
	// CategoryFilterMatch is a deterministic rule firing. NOT offered to the
	// model: it must not be able to claim a viewer tripped an operator's
	// filter when no filter matched.
	CategoryFilterMatch Category = "filter_match"
	// CategoryFlood is the history checker: rate, repetition, shouting,
	// mention spam. Also not offered to the model.
	CategoryFlood Category = "flood"
	// CategoryDoxxing is personal information shared about someone: a phone
	// number, an email address, an IP address. Pack checker only.
	CategoryDoxxing Category = "doxxing"
	// CategoryScam is a lure or a phishing shape: giveaway and crypto bait,
	// fake platform support, shortened and lookalike links. Pack checker only.
	// The model's "spam" maps to it, but the two stay distinct so a pack ladder
	// can be armed without arming the model's.
	CategoryScam Category = "scam"
	// CategoryImpersonation is an account named to pass for a protected one.
	// Pack checker only, and held, never acted on: the name might be a fan's.
	CategoryImpersonation Category = "impersonation"
	// CategorySelfHarm is a message expressing intent or a wish to self-harm.
	// Offered to the model only on installs with protection on (spec D14), and
	// never above a hold.
	CategorySelfHarm Category = "self_harm"
	// CategoryRaid is the same text from many accounts at once. Pack checker
	// only; its own category so a ladder can be armed for it separately.
	CategoryRaid Category = "raid"
)

// AllCategories is every Category declared above, in declaration order.
//
// Kept in step with the const block by an AST guard rather than by care --
// TestAllCategoriesIsTheWholeConstBlock parses this file and compares -- for
// the same reason events.AllTypes() is: a list a human maintains is exactly the
// shape that silently falls behind, and a Category missing from it is one whose
// platform-facing sentence nobody was required to write.
func AllCategories() []Category {
	return []Category{
		CategoryHarassment, CategoryHateSpeech, CategoryThreat,
		CategorySexualContent, CategorySpam, CategoryOther,
		CategoryFilterMatch, CategoryFlood,
		CategoryDoxxing, CategoryScam, CategoryImpersonation, CategorySelfHarm, CategoryRaid,
	}
}

// ModelCategories is the subset the model may choose from.
//
// Narrower than AllCategories, and that is the point: the two categories that
// assert a DETERMINISTIC checker fired are unavailable to the probabilistic
// one. Both the system prompt and ParseModelCategory are built from this slice,
// so the list the model is shown and the list the parser accepts cannot drift
// apart -- a drift that would show up as either a permanently rejected verdict
// or an accepted value nobody meant to offer.
func ModelCategories() []Category {
	return []Category{
		CategoryHarassment, CategoryHateSpeech, CategoryThreat,
		CategorySexualContent, CategorySpam, CategoryOther,
	}
}

// ModelCategoriesFor is ModelCategories, plus self_harm for an install with the
// protection pack on (spec D14). The Jev request and its parse take the list as
// a parameter so an existing model user's request does not change on upgrade.
func ModelCategoriesFor(selfHarm bool) []Category {
	cs := ModelCategories()
	if selfHarm {
		cs = append(cs, CategorySelfHarm)
	}
	return cs
}

// ParseCategoryIn is ParseModelCategory against an explicit offered list. The
// same rejecting half: no closest match and no default.
func ParseCategoryIn(s string, offered []Category) (Category, bool) {
	c := Category(strings.ToLower(strings.TrimSpace(s)))
	for _, x := range offered {
		if x == c {
			return c, true
		}
	}
	return "", false
}

// categoryReasons is the whole vocabulary that may reach a third party.
//
// Short by necessity: Kick caps a ban reason at 100 characters and truncates
// (Twitch at 1000), and a sentence cut in half on a permanent record reads as
// carelessness. TestEveryCategoryHasAShortPlatformReason holds the length.
var categoryReasons = map[Category]string{
	CategoryHarassment:    "Automated moderation: harassment.",
	CategoryHateSpeech:    "Automated moderation: hate speech.",
	CategoryThreat:        "Automated moderation: threats or violence.",
	CategorySexualContent: "Automated moderation: sexual content.",
	CategorySpam:          "Automated moderation: spam or scams.",
	CategoryOther:         "Automated moderation: chat rules.",
	CategoryFilterMatch:   "Automated moderation: matched a chat filter.",
	CategoryFlood:         "Automated moderation: flooding or repetition.",
	CategoryDoxxing:       "Automated moderation: sharing personal information.",
	CategoryScam:          "Automated moderation: scam or phishing.",
	CategoryImpersonation: "Automated moderation: impersonation.",
	CategorySelfHarm:      "Automated moderation: self-harm.",
	CategoryRaid:          "Automated moderation: coordinated flooding.",
}

// unclassifiedReason covers the zero Category, so a Finding built by a future
// checker that forgot to set one still produces a sentence rather than an empty
// reason field -- and still cannot produce free text.
const unclassifiedReason = "Automated moderation."

// Reason is the fixed, operator-neutral sentence for this category.
//
// Total by construction: an unknown or zero Category yields the generic
// sentence rather than the empty string, because the alternative -- returning
// "" and letting a caller fall back to something richer -- is exactly the hole
// this type closes.
func (c Category) Reason() string {
	if r, ok := categoryReasons[c]; ok {
		return r
	}
	return unclassifiedReason
}

// KnownCategory reports whether a category is one this build understands, in
// the same shape as KnownActions and KnownChecker.
func KnownCategory(c Category) bool {
	for _, x := range AllCategories() {
		if x == c {
			return true
		}
	}
	return false
}

// ParseModelCategory turns what the model said into a Category, or reports that
// it said something else.
//
// The rejecting half of the device. Case and surrounding space are forgiven,
// because those are formatting rather than meaning and refusing them would
// spend the fail-open budget on nothing; the value itself is not. There is
// deliberately no "closest match" and no default -- a fuzzy match is how an
// invented label becomes an accepted one.
func ParseModelCategory(s string) (Category, bool) {
	return ParseCategoryIn(s, ModelCategories())
}

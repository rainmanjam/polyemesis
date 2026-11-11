package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/automod/pack"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// Turning the stored protection block into a running pack checker and ladder.
//
// The block is the operator's DIFF over the built-in pack (db.AutomodProtection),
// so this is where a diff meets the pack's own defaults: a category the diff
// does not mention keeps the pack's setting, whatever that is in this release.

// protectionOn reports whether the pack runs. The one definition: the
// configured guard, ApplyAutomod and the explain route all ask it.
func protectionOn(a db.AutomodSettings) bool { return a.Protection.State == db.ProtectionOn }

// protectionCategories are the keys a stored category may use: the pack's
// term categories and its detector categories.
var protectionCategories = map[string]bool{
	pack.CategoryHate: true, pack.CategoryProfanity: true, pack.CategoryDoxxing: true,
	pack.CategoryScam: true, pack.CategoryFlood: true, pack.CategoryImpersonation: true,
	pack.CategoryRaid: true,
}

// groupCategory is the category a pack group belongs to.
func groupCategory(group string) string {
	if group == pack.GroupNearDuplicate {
		return pack.CategoryFlood
	}
	cat, _, _ := strings.Cut(group, "/")
	return cat
}

// groupShort is the name of a group inside its category: "address" for
// doxxing/address, and "near_duplicate", "impersonation" or "raid" for the
// groups that have no slash.
func groupShort(group string) string {
	if group == pack.GroupNearDuplicate {
		return "near_duplicate"
	}
	if _, det, ok := strings.Cut(group, "/"); ok {
		return det
	}
	return group
}

// historyDetectors are the history checker's detectors the hold policy may
// name. They belong to the flood category and have no pack group.
var historyDetectors = []string{
	automod.DetectorRepeats, automod.DetectorMessageRate, automod.DetectorMentions,
	automod.DetectorLinks, automod.DetectorCaps,
}

// resolveHoldKey maps a holdDetectors key (a full group, or a short name) to
// what automod.ProtectionConfig.Hold takes. ok is false for a name no detector
// has.
func resolveHoldKey(k string) (string, bool) {
	for _, g := range pack.Groups() {
		if k == g {
			return g, true
		}
	}
	for _, g := range pack.Groups() {
		if k == groupShort(g) {
			return g, true
		}
	}
	for _, h := range historyDetectors {
		if k == h {
			return k, true
		}
	}
	return "", false
}

// protectionVocabProblems checks the names a stored block uses against the
// pack's own vocabulary. Here rather than in db's problems() because the
// vocabulary lives in internal/automod/pack and in internal/automod.
func protectionVocabProblems(p db.AutomodProtection) []string {
	var probs []string
	probs = append(probs, packVocabProblems(p)...)
	probs = append(probs, categoryVocabProblems(p)...)
	probs = append(probs, ceilingVocabProblems(p)...)
	probs = append(probs, holdDetectorVocabProblems(p)...)
	probs = append(probs, addTermVocabProblems(p)...)
	return probs
}

// vocabSortedKeys returns a map's keys in sorted order, so the problems that name
// them come out in a stable order.
func vocabSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// packVocabProblems names a pack this build does not have and a negative version.
func packVocabProblems(p db.AutomodProtection) []string {
	var probs []string
	if p.Pack != "" && p.Pack != db.DefaultProtectionPack {
		probs = append(probs, fmt.Sprintf("protection pack %q is not one this build has", p.Pack))
	}
	if p.PackVersion < 0 {
		probs = append(probs, "protection packVersion cannot be negative")
	}
	return probs
}

// categoryVocabProblems names a category the pack does not have, and a detector
// that does not belong to a category that does.
func categoryVocabProblems(p db.AutomodProtection) []string {
	var probs []string
	for _, c := range vocabSortedKeys(p.Categories) {
		if !protectionCategories[c] {
			probs = append(probs, fmt.Sprintf("protection category %q is not one the pack has", c))
			continue
		}
		for _, d := range vocabSortedKeys(p.Categories[c].Detectors) {
			if _, ok := groupOf(c, d); !ok {
				probs = append(probs, fmt.Sprintf("protection category %q has no detector %q", c, d))
			}
		}
	}
	return probs
}

// ceilingVocabProblems names a ceiling for a category that is not one of the
// known ceiling keys.
func ceilingVocabProblems(p db.AutomodProtection) []string {
	var probs []string
	allowed := map[string]bool{"self_harm": true}
	for _, k := range automod.CeilingKeys() {
		allowed[k] = true
	}
	for _, k := range vocabSortedKeys(p.Ceilings) {
		if !allowed[k] {
			probs = append(probs, fmt.Sprintf("protection ceiling names unknown category %q", k))
		}
	}
	return probs
}

// holdDetectorVocabProblems names a hold detector that does not resolve.
func holdDetectorVocabProblems(p db.AutomodProtection) []string {
	var probs []string
	for _, k := range vocabSortedKeys(p.HoldDetectors) {
		if _, ok := resolveHoldKey(k); !ok {
			probs = append(probs, fmt.Sprintf("protection holdDetectors names unknown detector %q", k))
		}
	}
	return probs
}

// addTermVocabProblems checks each added term's tier against its category.
func addTermVocabProblems(p db.AutomodProtection) []string {
	var probs []string
	for _, t := range p.AddTerms {
		switch t.Category {
		case pack.CategoryHate:
			if t.Tier != pack.TierSevere && t.Tier != pack.TierContextual {
				probs = append(probs, "an added hate term needs tier severe or contextual")
			}
		case pack.CategoryProfanity:
			if t.Tier != pack.TierMild && t.Tier != pack.TierStrong {
				probs = append(probs, "an added profanity term needs tier mild or strong")
			}
		default:
			probs = append(probs, "an added term needs category hate_speech or profanity")
		}
	}
	return probs
}

// groupOf finds the pack group a (category, detector) pair names.
func groupOf(category, detector string) (string, bool) {
	for _, g := range pack.Groups() {
		if groupCategory(g) == category && groupShort(g) == detector {
			return g, true
		}
	}
	return "", false
}

// packConfigFromSettings is the pack's configuration for a stored block: the
// pack's defaults with the operator's diff applied.
func packConfigFromSettings(p db.AutomodProtection) (pack.Config, map[string]bool) {
	cfg := pack.Config{
		Reserved:     pack.DefaultReservedPolicy(),
		Categories:   map[string]bool{},
		Detectors:    map[string]bool{},
		AllowTerms:   append([]string(nil), p.AllowTerms...),
		AllowDomains: append([]string(nil), p.AllowDomains...),
		AllowIDs:     map[string]bool{},
	}
	for _, t := range p.AddTerms {
		cfg.AddTerms = append(cfg.AddTerms, pack.AddTerm{Term: t.Term, Category: t.Category, Tier: t.Tier})
	}
	for _, id := range p.Impersonation.AllowIDs {
		cfg.AllowIDs[id] = true
	}
	for cat, c := range p.Categories {
		applyProtectionCategory(&cfg, cat, c)
	}
	hold := map[string]bool{}
	for k, v := range p.HoldDetectors {
		if key, ok := resolveHoldKey(k); ok {
			hold[key] = v
		}
	}
	return cfg, hold
}

// applyProtectionCategory writes one protection category into the pack config:
// the term categories are switched as categories, every other category through
// its groups.
func applyProtectionCategory(cfg *pack.Config, cat string, c db.ProtectionCategory) {
	off := c.Enabled != nil && !*c.Enabled
	// The two term categories are switched as categories; every other
	// category is its groups. A disabled category switches all of them off,
	// and wins over a detector inside it.
	if cat == pack.CategoryHate || cat == pack.CategoryProfanity {
		if c.Enabled != nil {
			cfg.Categories[cat] = *c.Enabled
		}
	}
	for _, g := range pack.Groups() {
		if groupCategory(g) != cat {
			continue
		}
		if off {
			cfg.Detectors[g] = false
		} else if v, ok := c.Detectors[groupShort(g)]; ok {
			cfg.Detectors[g] = v
		}
	}
	// A profanity group follows its category, not a detector entry.
	if cat == pack.CategoryProfanity {
		delete(cfg.Detectors, pack.GroupProfanityMild)
		delete(cfg.Detectors, pack.GroupProfanityStrong)
	}
}

// ladderFromSettings is the strike ladder for a stored block.
func ladderFromSettings(p db.AutomodProtection, st *automod.Strikes) *automod.Ladder {
	l := &automod.Ladder{
		Strikes:        st,
		Window:         time.Duration(p.StrikeWindowSeconds) * time.Second,
		TimeoutSeconds: p.LadderTimeoutSeconds,
		Ceilings:       map[string]automod.Rung{},
	}
	for k, v := range p.Ceilings {
		if r, ok := automod.ParseRung(v); ok && k != "self_harm" {
			l.Ceilings[k] = r
		}
	}
	return l
}

// protectionFromSettings builds the pack checker, or nil when the pack is not
// on. The windowed trackers and the name set come from ps and are kept across
// saves; ps may be nil, which gives the checker none (the explain route).
func protectionFromSettings(a db.AutomodSettings, hl automod.HistoryLimits, ps *automod.Persistent) (*automod.Protection, error) {
	if !protectionOn(a) {
		return nil, nil
	}
	cfg, hold := packConfigFromSettings(a.Protection)
	if ps != nil {
		cfg.Raid, cfg.NearDup, cfg.Names = ps.Raid, ps.NearDup, ps.Names
		// The operator's own list replaces the static half, so removing a name
		// takes effect; the learned half is untouched.
		ps.Names.SetStatic(a.Protection.Impersonation.Protect)
	} else {
		cfg.Names = pack.NewNameSet(a.Protection.Impersonation.Protect)
	}
	return automod.NewProtection(automod.ProtectionConfig{Pack: cfg, Hold: hold, History: &hl})
}

// sameProtection reports whether two stored blocks are the same document, with
// the derived offer ignored: the client echoes it back, and it must not make an
// unchanged block look edited.
func sameProtection(a, b db.AutomodProtection) bool {
	a.Offer, b.Offer = false, false
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

// protectionChangeProblems refuses a protection block this build cannot run,
// but only when the save CHANGES the block. An install carrying a category or
// detector name a newer build wrote, or one a later pack dropped, must still be
// able to save its listener port; the same reasoning scopes the rule-compile
// check and the cell-arming check to a change.
func protectionChangeProblems(stored, sent db.AutomodProtection) error {
	if sameProtection(stored, sent) {
		return nil
	}
	if probs := protectionVocabProblems(sent); len(probs) > 0 {
		return badRequestError{"automod: " + strings.Join(probs, "; ")}
	}
	return nil
}

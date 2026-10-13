// Package moved pairs destroyed resource addresses with created ones and
// turns the confident pairs into Terraform moved blocks.
package moved

import (
	"math"
	"path"
	"reflect"
	"sort"
	"strings"

	"github.com/moveeeax/tf-moved-block-gen/internal/tfplan"
)

// The three similarity signals and their weights. They sum to 1, so a
// confidence is always in [0,1] and thresholds are easy to reason about.
const (
	weightAttrs = 0.75
	weightName  = 0.15
	weightIndex = 0.10

	// neutralAttrs is used when a pair has nothing comparable at all: every
	// attribute of the created resource is still unknown. A pair like that can
	// only reach 0.4*0.75+0.15+0.10 = 0.55, which stays under the default
	// threshold unless the operator lowers it deliberately.
	neutralAttrs = 0.4
)

// volatile attributes are server-assigned, so comparing them says nothing
// about whether two addresses describe the same infrastructure.
var volatile = map[string]bool{
	"id":            true,
	"arn":           true,
	"unique_id":     true,
	"owner_id":      true,
	"created_at":    true,
	"created_date":  true,
	"creation_date": true,
	"last_modified": true,
	"etag":          true,
	"version_id":    true,
	"self_link":     true,
}

// identifying attributes carry most of a resource's identity, so a match or a
// mismatch on them counts triple.
var identifying = map[string]bool{
	"name":       true,
	"tags":       true,
	"bucket":     true,
	"identifier": true,
	"key":        true,
	"path":       true,
	"repository": true,
	"family":     true,
	"domain":     true,
}

func nameLike(key string) bool {
	return identifying[key] || strings.HasSuffix(key, "_name")
}

// Options tunes the pairing.
type Options struct {
	MinConfidence float64
	SameNameOnly  bool
	Ignore        []string
}

// Move is one proposed moved block.
type Move struct {
	From       string   `json:"from"`
	To         string   `json:"to"`
	Type       string   `json:"type"`
	Confidence float64  `json:"confidence"`
	Matched    []string `json:"matched_attributes"`
	Mismatched []string `json:"mismatched_attributes,omitempty"`
}

// Finding is a destroy (or replace) reported without a moved block.
type Finding struct {
	Address string `json:"address"`
	Type    string `json:"type"`
	Reason  string `json:"reason"`
}

// Result is the whole analysis of one plan.
type Result struct {
	Moves        []Move    `json:"moved"`
	Unexplained  []Finding `json:"unexplained_destroys"`
	Replacements []Finding `json:"replacements,omitempty"`
	LeftoverAdds []string  `json:"unmatched_creates,omitempty"`
}

// Analyze classifies every resource change and returns the proposed moves plus
// the destroys nothing explains.
func Analyze(p *tfplan.Plan, opt Options) Result {
	res := Result{Moves: []Move{}, Unexplained: []Finding{}}

	var deletes, creates []tfplan.ResourceChange
	for _, rc := range p.ResourceChanges {
		// Only managed resources can be moved; data sources have no state to
		// carry over.
		if rc.Mode != "managed" || ignored(rc.Address, opt.Ignore) {
			continue
		}
		switch rc.Change.Kind() {
		case tfplan.KindDelete:
			deletes = append(deletes, rc)
		case tfplan.KindCreate:
			creates = append(creates, rc)
		case tfplan.KindReplace:
			res.Replacements = append(res.Replacements, Finding{
				Address: rc.Address,
				Type:    rc.Type,
				Reason:  "replaced in place: the address is unchanged, so no moved block applies",
			})
		}
	}

	var candidates []Move
	for _, d := range deletes {
		for _, c := range creates {
			if d.Type != c.Type || d.Address == c.Address {
				continue
			}
			if opt.SameNameOnly && d.Name != c.Name {
				continue
			}
			conf, matched, mismatched := score(d, c)
			if conf < opt.MinConfidence {
				continue
			}
			candidates = append(candidates, Move{
				From: d.Address, To: c.Address, Type: d.Type,
				Confidence: conf, Matched: matched, Mismatched: mismatched,
			})
		}
	}

	// Greedy assignment, best score first. Ties break on the addresses so the
	// output is identical across runs on the same input.
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})

	usedFrom := map[string]bool{}
	usedTo := map[string]bool{}
	for _, c := range candidates {
		if usedFrom[c.From] || usedTo[c.To] {
			continue
		}
		usedFrom[c.From], usedTo[c.To] = true, true
		res.Moves = append(res.Moves, c)
	}
	sort.Slice(res.Moves, func(i, j int) bool { return res.Moves[i].From < res.Moves[j].From })

	createdTypes := map[string]int{}
	for _, c := range creates {
		createdTypes[c.Type]++
	}
	for _, d := range deletes {
		if usedFrom[d.Address] {
			continue
		}
		reason := "nothing of this type is created in this plan"
		if createdTypes[d.Type] > 0 {
			reason = "no create of this type reached the confidence threshold"
		}
		res.Unexplained = append(res.Unexplained, Finding{Address: d.Address, Type: d.Type, Reason: reason})
	}
	sort.Slice(res.Unexplained, func(i, j int) bool { return res.Unexplained[i].Address < res.Unexplained[j].Address })

	for _, c := range creates {
		if !usedTo[c.Address] {
			res.LeftoverAdds = append(res.LeftoverAdds, c.Address)
		}
	}
	sort.Strings(res.LeftoverAdds)
	sort.Slice(res.Replacements, func(i, j int) bool { return res.Replacements[i].Address < res.Replacements[j].Address })

	return res
}

// score rates one (destroy, create) pair in [0,1].
func score(del, cre tfplan.ResourceChange) (float64, []string, []string) {
	var matchedWeight, totalWeight float64
	var matched, mismatched []string

	for _, k := range comparableKeys(del, cre) {
		w := 1.0
		if nameLike(k) {
			w = 3.0
		}
		totalWeight += w
		if reflect.DeepEqual(del.Change.Before[k], cre.Change.After[k]) {
			matchedWeight += w
			matched = append(matched, k)
		} else {
			mismatched = append(mismatched, k)
		}
	}

	attrs := neutralAttrs
	if totalWeight > 0 {
		attrs = matchedWeight / totalWeight
	}

	conf := weightAttrs*attrs +
		weightName*nameSimilarity(del.Name, cre.Name) +
		weightIndex*indexScore(del.Index, cre.Index)

	return math.Round(conf*10000) / 10000, matched, mismatched
}

// comparableKeys returns the sorted attribute names present on both sides and
// actually known after the apply. Anything still unknown in the plan would
// compare as a mismatch for no reason, so it is skipped.
func comparableKeys(del, cre tfplan.ResourceChange) []string {
	before, after, unknown := del.Change.Before, cre.Change.After, cre.Change.AfterUnknown
	keys := make([]string, 0, len(before))
	for k, bv := range before {
		if volatile[k] || isUnknown(unknown[k]) {
			continue
		}
		av, ok := after[k]
		if !ok {
			continue
		}
		if bv == nil && av == nil {
			continue // two nulls carry no signal
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isUnknown reads one entry of after_unknown. Terraform writes `true` for a
// wholly unknown attribute and a nested object or array when only part of it is
// unknown; a partially unknown attribute is treated as unknown too, which is
// the conservative choice.
func isUnknown(v any) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}

// indexScore compares count/for_each keys. Different keys still pair (that is
// exactly an index shift) but score lower than an exact match.
func indexScore(a, b any) float64 {
	switch {
	case a == nil && b == nil:
		return 1
	case a == nil || b == nil:
		return 0.2
	case reflect.DeepEqual(a, b):
		return 1
	default:
		return 0.4
	}
}

// nameSimilarity is the Dice coefficient over character bigrams of the two
// local resource names. A module move keeps the name and scores 1; a genuine
// rename like web -> api scores 0 and has to win on attributes alone.
func nameSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}
	left, right := bigrams(a), bigrams(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	var shared, total int
	for g, n := range left {
		total += n
		if m, ok := right[g]; ok {
			shared += min(n, m)
		}
	}
	for _, n := range right {
		total += n
	}
	return 2 * float64(shared) / float64(total)
}

func bigrams(s string) map[string]int {
	r := []rune(strings.ToLower(s))
	out := map[string]int{}
	switch {
	case len(r) == 0:
		return out
	case len(r) == 1:
		out[string(r)] = 1
		return out
	}
	for i := 0; i+1 < len(r); i++ {
		out[string(r[i:i+2])]++
	}
	return out
}

// ignored reports whether an address matches any --ignore glob.
func ignored(address string, patterns []string) bool {
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if ok, err := path.Match(p, address); err == nil && ok {
			return true
		}
	}
	return false
}

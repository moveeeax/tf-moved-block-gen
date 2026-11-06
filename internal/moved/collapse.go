package moved

import (
	"sort"
	"strings"

	"github.com/moveeeax/tf-moved-block-gen/internal/tfplan"
)

// moduleCall is one `module.NAME[KEY]` step of a resource address. Key keeps
// its brackets ("[0]", `["eu"]`) and is empty for a call without count or
// for_each.
type moduleCall struct {
	name, key string
}

// parseAddress splits a resource address into its module calls and the
// address inside the innermost module. A for_each key may hold dots and
// brackets, so the key is read as a quoted string rather than split on dots.
func parseAddress(addr string) (calls []moduleCall, local string, ok bool) {
	rest := addr
	for strings.HasPrefix(rest, "module.") {
		rest = rest[len("module."):]
		end := strings.IndexAny(rest, ".[")
		if end <= 0 {
			return nil, "", false
		}
		c := moduleCall{name: rest[:end]}
		rest = rest[end:]
		if strings.HasPrefix(rest, "[") {
			n := keyLen(rest)
			if n == 0 {
				return nil, "", false
			}
			c.key, rest = rest[:n], rest[n:]
		}
		if !strings.HasPrefix(rest, ".") {
			return nil, "", false
		}
		rest = rest[1:]
		calls = append(calls, c)
	}
	if rest == "" {
		return nil, "", false
	}
	return calls, rest, true
}

// keyLen returns the length of the bracketed key at the start of s,
// including both brackets, or 0 when it is malformed.
func keyLen(s string) int {
	if len(s) > 1 && s[1] == '"' {
		for i := 2; i < len(s); i++ {
			switch s[i] {
			case '\\':
				i++
			case '"':
				if i+1 < len(s) && s[i+1] == ']' {
					return i + 2
				}
				return 0
			}
		}
		return 0
	}
	if i := strings.IndexByte(s, ']'); i > 0 {
		return i + 1
	}
	return 0
}

func joinCalls(calls []moduleCall) string {
	parts := make([]string, len(calls))
	for i, c := range calls {
		parts[i] = "module." + c.name + c.key
	}
	return strings.Join(parts, ".")
}

// split is one way to read an address as `<module><rest>`. The module is
// either a whole call (`module.app`, rest starting with the instance key) or
// one instance of it (`module.app[0]`, rest starting with ".").
type split struct {
	call, rest string
}

// splits lists every module call and module instance an address can be moved
// with, from the outermost call inward.
func splits(addr string) []split {
	calls, local, ok := parseAddress(addr)
	if !ok {
		return nil
	}
	out := make([]split, 0, len(calls))
	for i, c := range calls {
		call := "module." + c.name
		if i > 0 {
			call = joinCalls(calls[:i]) + "." + call
		}
		rest := c.key
		if inner := joinCalls(calls[i+1:]); inner != "" {
			rest += "." + inner
		}
		out = append(out, split{call: call, rest: rest + "." + local})
		if c.key != "" {
			out = append(out, split{call: call + c.key, rest: strings.TrimPrefix(rest, c.key) + "." + local})
		}
	}
	return out
}

// under reports whether addr lies inside the module call, and returns the
// part of the address after the call name.
func under(addr, call string) (string, bool) {
	if !strings.HasPrefix(addr, call) {
		return "", false
	}
	rest := addr[len(call):]
	if strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, "[") {
		return rest, true
	}
	return "", false
}

// collapse replaces the per-resource moves of a renamed module call with one
// module-level moved block. The whole call (`module.X` to `module.Y`, which
// covers every count/for_each instance) is tried first; when only some
// instances or a nested call moved whole, those collapse on their own. A
// module qualifies when every change under the old
// call is a destroy that moves to the same relative address under the new
// call, and every managed change under the new call is a create that came
// from there. Anything else in either call (an update, a no-op, an unmatched
// or ignored destroy or create) means the module was not simply renamed, and
// the per-resource blocks are kept. Data sources under the new call are
// re-read after a rename and do not count against it.
func collapse(changes []tfplan.ResourceChange, moves []Move) []Move {
	byFrom := map[string]Move{}
	byTo := map[string]Move{}
	for _, m := range moves {
		byFrom[m.From] = m
		byTo[m.To] = m
	}

	type pair struct{ from, to string }
	seen := map[pair]bool{}
	var cands []pair
	for _, m := range moves {
		for _, f := range splits(m.From) {
			for _, t := range splits(m.To) {
				p := pair{f.call, t.call}
				if f.rest != t.rest || seen[p] || nested(p.from, p.to) {
					continue
				}
				seen[p] = true
				cands = append(cands, p)
			}
		}
	}
	// Outermost calls first, and a call before its instances, so a whole
	// subtree collapses into one block rather than one block per nested call.
	sort.Slice(cands, func(i, j int) bool {
		di, dj := strings.Count(cands[i].from, "module."), strings.Count(cands[j].from, "module.")
		if di != dj {
			return di < dj
		}
		if cands[i].from != cands[j].from {
			return cands[i].from < cands[j].from
		}
		return cands[i].to < cands[j].to
	})

	used := map[string]bool{}
	var out []Move
	for _, c := range cands {
		group, ok := wholeCallMove(changes, byFrom, byTo, c.from, c.to)
		if !ok {
			continue
		}
		clash := false
		for _, m := range group {
			clash = clash || used[m.From]
		}
		if clash {
			continue
		}
		conf := 1.0
		for _, m := range group {
			used[m.From] = true
			conf = min(conf, m.Confidence)
		}
		sort.Slice(group, func(i, j int) bool { return group[i].From < group[j].From })
		out = append(out, Move{From: c.from, To: c.to, Type: "module", Confidence: conf, Grouped: group})
	}
	if len(out) == 0 {
		return moves
	}
	for _, m := range moves {
		if !used[m.From] {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

// nested reports whether one call lies inside the other; a module cannot be
// moved into or out of itself.
func nested(a, b string) bool {
	_, ab := under(a, b)
	_, ba := under(b, a)
	return a == b || ab || ba
}

// wholeCallMove checks that from was renamed to to as a whole and returns the
// resource moves it covers.
func wholeCallMove(changes []tfplan.ResourceChange, byFrom, byTo map[string]Move, from, to string) ([]Move, bool) {
	var group []Move
	inGroup := map[string]bool{}
	for _, rc := range changes {
		if rest, ok := under(rc.Address, from); ok {
			m, paired := byFrom[rc.Address]
			if rc.Mode != "managed" || rc.Change.Kind() != tfplan.KindDelete || !paired || m.To != to+rest {
				return nil, false
			}
			// A deposed object shares its address with the current one.
			if !inGroup[m.From] {
				inGroup[m.From] = true
				group = append(group, m)
			}
		}
		if rest, ok := under(rc.Address, to); ok {
			if rc.Mode != "managed" {
				continue
			}
			m, paired := byTo[rc.Address]
			if rc.Change.Kind() != tfplan.KindCreate || !paired || m.From != from+rest {
				return nil, false
			}
		}
	}
	return group, len(group) > 0
}

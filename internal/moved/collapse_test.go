package moved

import (
	"strings"
	"testing"

	"github.com/moveeeax/tf-moved-block-gen/internal/tfplan"
)

func TestWholeModuleRenameCollapsesToOneBlock(t *testing.T) {
	res := analyze(t, "module_rename.json", defaults())

	if len(res.Moves) != 1 {
		t.Fatalf("want 1 module-level block, got %d: %+v", len(res.Moves), res.Moves)
	}
	m := res.Moves[0]
	if m.From != "module.app" || m.To != "module.edge" || m.Type != "module" {
		t.Fatalf("want module.app -> module.edge, got %s -> %s (%s)", m.From, m.To, m.Type)
	}
	// Both count instances, a nested module and a for_each key with a dot in it.
	if len(m.Grouped) != 4 {
		t.Fatalf("want 4 grouped resource pairs, got %+v", m.Grouped)
	}
	lowest := 1.0
	for _, g := range m.Grouped {
		lowest = min(lowest, g.Confidence)
	}
	if m.Confidence != lowest {
		t.Errorf("module confidence %.4f, want the lowest grouped confidence %.4f", m.Confidence, lowest)
	}

	hcl := RenderHCL(res)
	if !strings.Contains(hcl, "moved {\n  from = module.app\n  to   = module.edge\n}\n") {
		t.Errorf("module block missing:\n%s", hcl)
	}
	if !strings.Contains(hcl, "lowest of 4 resource pairs") {
		t.Errorf("comment should give the grouped count:\n%s", hcl)
	}
	if strings.Contains(hcl, "aws_security_group") {
		t.Errorf("per-resource blocks should be gone:\n%s", hcl)
	}
	// The report still lists every resource that moves.
	s := RenderSummary(res)
	if !strings.Contains(s, "module  module.app -> module.edge") ||
		!strings.Contains(s, `rename  module.app[1].module.net.aws_subnet.this["eu.west-1a"]`) {
		t.Errorf("summary lost the grouped pairs:\n%s", s)
	}
}

func TestNoCollapseKeepsPerResourceBlocks(t *testing.T) {
	res := analyze(t, "module_rename.json", Options{MinConfidence: 0.6, NoCollapse: true})
	if len(res.Moves) != 4 {
		t.Fatalf("want 4 per-resource blocks, got %d: %+v", len(res.Moves), res.Moves)
	}
	for _, m := range res.Moves {
		if len(m.Grouped) != 0 || !strings.HasPrefix(m.From, "module.app[") {
			t.Errorf("unexpected move %+v", m)
		}
	}
}

// with appends one managed change to the fixture.
func with(t *testing.T, addr, mod string, actions ...string) *tfplan.Plan {
	t.Helper()
	p := load(t, "module_rename.json")
	p.ResourceChanges = append(p.ResourceChanges, tfplan.ResourceChange{
		Address: addr, ModuleAddress: mod, Mode: "managed", Type: "aws_iam_role", Name: "x",
		Change: tfplan.Change{Actions: actions},
	})
	return p
}

// Anything else going on in either module means it was not simply renamed;
// collapsing would move resources the plan does not move.
func TestCollapseNeedsTheWholeModule(t *testing.T) {
	cases := map[string]*tfplan.Plan{
		"unchanged resource left in the old module": with(t, "module.app[1].aws_iam_role.x", "module.app[1]", "no-op"),
		"update in the old module":                  with(t, "module.app[0].aws_iam_role.x", "module.app[0]", "update"),
		"real destroy in the old module":            with(t, "module.app[0].aws_iam_role.x", "module.app[0]", "delete"),
		"new resource in the new module":            with(t, "module.edge[1].aws_iam_role.x", "module.edge[1]", "create"),
		"replacement in the new module":             with(t, "module.edge[0].aws_iam_role.x", "module.edge[0]", "delete", "create"),
	}
	for name, p := range cases {
		res := Analyze(p, defaults())
		for _, m := range res.Moves {
			if m.From == "module.app" {
				t.Errorf("%s: collapsed anyway: %+v", name, res.Moves)
			}
		}
		// The four renames are still proposed one way or another.
		covered := 0
		for _, m := range res.Moves {
			if len(m.Grouped) > 0 {
				covered += len(m.Grouped)
			} else {
				covered++
			}
		}
		if covered != 4 {
			t.Errorf("%s: want 4 renames covered, got %d: %+v", name, covered, res.Moves)
		}
	}
}

// An instance kept in the old module blocks the call-level block, but an
// instance or nested module that moved whole still collapses on its own.
func TestCollapseFallsBackToNarrowerCalls(t *testing.T) {
	res := Analyze(with(t, "module.app[1].aws_iam_role.x", "module.app[1]", "no-op"), defaults())
	var froms []string
	for _, m := range res.Moves {
		froms = append(froms, m.From)
	}
	got := strings.Join(froms, " | ")
	want := `module.app[0] | module.app[1].aws_security_group.lb | module.app[1].module.net`
	if got != want {
		t.Errorf("moves = %q, want %q", got, want)
	}
	hcl := RenderHCL(res)
	if !strings.Contains(hcl, "| whole module instance\nmoved {\n  from = module.app[0]\n  to   = module.edge[0]\n}") {
		t.Errorf("instance-level block missing or mislabelled:\n%s", hcl)
	}
}

func TestIgnoredResourceBlocksCollapse(t *testing.T) {
	res := analyze(t, "module_rename.json", Options{MinConfidence: 0.6, Ignore: []string{"module.app[[]0].aws_*"}})
	for _, m := range res.Moves {
		if m.From == "module.app" {
			t.Fatalf("an ignored resource must keep the module from collapsing: %+v", res.Moves)
		}
	}
}

// A module whose resources moved to different keys is not a call rename.
func TestCollapseNeedsMatchingKeys(t *testing.T) {
	p := load(t, "module_rename.json")
	for i, rc := range p.ResourceChanges {
		if strings.HasPrefix(rc.Address, "module.edge[1]") {
			p.ResourceChanges[i].Address = strings.Replace(rc.Address, "module.edge[1]", "module.edge[7]", 1)
		}
	}
	res := Analyze(p, defaults())
	for _, m := range res.Moves {
		if m.From == "module.app" {
			t.Fatalf("keys differ, must not collapse to the call: %+v", res.Moves)
		}
	}
}

func TestSingleResourceModuleRename(t *testing.T) {
	res := analyze(t, "module_move.json", defaults())
	if len(res.Moves) != 1 || res.Moves[0].From != "module.app" || res.Moves[0].To != "module.edge" {
		t.Fatalf("want module.app -> module.edge, got %+v", res.Moves)
	}
	if hcl := RenderHCL(res); !strings.Contains(hcl, "(1 resource pair)") {
		t.Errorf("comment should name the single pair:\n%s", hcl)
	}
}

func TestParseAddress(t *testing.T) {
	cases := []struct {
		addr  string
		calls string
		local string
		ok    bool
	}{
		{"aws_instance.web", "", "aws_instance.web", true},
		{"module.a.aws_instance.web", "module.a", "aws_instance.web", true},
		{`module.a["x.y]z"].module.b[2].aws_instance.web[0]`, `module.a["x.y]z"].module.b[2]`, "aws_instance.web[0]", true},
		{`module.a["q\"]"].x.y`, `module.a["q\"]"]`, "x.y", true},
		{"module.a", "", "", false},
		{`module.a["unterminated.x.y`, "", "", false},
	}
	for _, c := range cases {
		calls, local, ok := parseAddress(c.addr)
		if ok != c.ok || joinCalls(calls) != c.calls || local != c.local {
			t.Errorf("parseAddress(%q) = %q, %q, %v; want %q, %q, %v",
				c.addr, joinCalls(calls), local, ok, c.calls, c.local, c.ok)
		}
	}
}

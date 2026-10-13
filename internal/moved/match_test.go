package moved

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/moveeeax/tf-moved-block-gen/internal/tfplan"
)

func load(t *testing.T, name string) *tfplan.Plan {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	p, err := tfplan.Load(f)
	if err != nil {
		t.Fatalf("load fixture %s: %v", name, err)
	}
	return p
}

func defaults() Options { return Options{MinConfidence: 0.6} }

func analyze(t *testing.T, name string, opt Options) Result {
	t.Helper()
	return Analyze(load(t, name), opt)
}

func TestRenameProducesExactlyOneMovedBlock(t *testing.T) {
	res := analyze(t, "rename.json", defaults())

	if len(res.Moves) != 1 {
		t.Fatalf("want 1 moved block, got %d: %+v", len(res.Moves), res.Moves)
	}
	m := res.Moves[0]
	if m.From != "aws_instance.web" || m.To != "aws_instance.api" {
		t.Errorf("want aws_instance.web -> aws_instance.api, got %s -> %s", m.From, m.To)
	}
	if m.Confidence < 0.8 {
		t.Errorf("identical attributes should score high, got %.2f", m.Confidence)
	}
	if len(res.Unexplained) != 0 {
		t.Errorf("want no unexplained destroys, got %+v", res.Unexplained)
	}
	// The identifying attributes have to show up in the comment, that is the
	// whole point of reporting what matched.
	joined := strings.Join(m.Matched, ",")
	for _, want := range []string{"ami", "instance_type", "tags"} {
		if !strings.Contains(joined, want) {
			t.Errorf("matched attributes %q missing %q", joined, want)
		}
	}
	// id and arn are server-assigned and must never count as evidence.
	for _, nope := range []string{"id", "arn", "private_ip"} {
		for _, got := range m.Matched {
			if got == nope {
				t.Errorf("volatile attribute %q used as evidence", nope)
			}
		}
	}
}

func TestModuleMoveKeepsFullAddresses(t *testing.T) {
	res := analyze(t, "module_move.json", defaults())

	if len(res.Moves) != 1 {
		t.Fatalf("want 1 moved block, got %d", len(res.Moves))
	}
	m := res.Moves[0]
	if m.From != "module.app.aws_security_group.lb" || m.To != "module.edge.aws_security_group.lb" {
		t.Fatalf("module prefixes lost: %s -> %s", m.From, m.To)
	}
	if m.Confidence <= 0.95 {
		t.Errorf("same name plus identical attributes should be near 1, got %.2f", m.Confidence)
	}
}

func TestIndexShiftPairsAcrossKeys(t *testing.T) {
	res := analyze(t, "index_shift.json", defaults())

	if len(res.Moves) != 1 {
		t.Fatalf("want 1 moved block, got %d", len(res.Moves))
	}
	m := res.Moves[0]
	if m.From != "aws_security_group_rule.allow[1]" || m.To != "aws_security_group_rule.allow[2]" {
		t.Fatalf("index keys lost: %s -> %s", m.From, m.To)
	}
	// A differing index should cost something, not everything.
	if m.Confidence >= 1.0 || m.Confidence < 0.8 {
		t.Errorf("index shift confidence %.2f outside the expected band", m.Confidence)
	}
}

func TestGenuineDestroyStaysUnexplained(t *testing.T) {
	res := analyze(t, "real_destroy.json", defaults())

	if len(res.Moves) != 0 {
		t.Fatalf("a destroy with no partner must not produce a moved block: %+v", res.Moves)
	}
	if len(res.Unexplained) != 1 || res.Unexplained[0].Address != "aws_s3_bucket.legacy_logs" {
		t.Fatalf("want the bucket reported as unexplained, got %+v", res.Unexplained)
	}
	if !strings.Contains(res.Unexplained[0].Reason, "type") {
		t.Errorf("reason should explain the missing type, got %q", res.Unexplained[0].Reason)
	}
	if len(res.LeftoverAdds) != 1 || res.LeftoverAdds[0] != "aws_instance.worker" {
		t.Errorf("want the unrelated create listed, got %+v", res.LeftoverAdds)
	}
}

func TestDifferentTypesNeverPair(t *testing.T) {
	res := analyze(t, "real_destroy.json", Options{MinConfidence: 0})
	for _, m := range res.Moves {
		t.Errorf("confidence 0 must still not cross resource types: %s -> %s", m.From, m.To)
	}
}

func TestLowSimilarityStaysUnexplained(t *testing.T) {
	res := analyze(t, "low_similarity.json", defaults())

	if len(res.Moves) != 0 {
		t.Fatalf("unrelated databases must not pair: %+v", res.Moves)
	}
	if len(res.Unexplained) != 1 {
		t.Fatalf("want 1 unexplained destroy, got %d", len(res.Unexplained))
	}
	if !strings.Contains(res.Unexplained[0].Reason, "confidence") {
		t.Errorf("reason should mention the threshold, got %q", res.Unexplained[0].Reason)
	}
}

func TestCreateIsUsedByAtMostOneMove(t *testing.T) {
	res := analyze(t, "duplicate.json", defaults())

	if len(res.Moves) != 1 {
		t.Fatalf("one create can back only one moved block, got %d: %+v", len(res.Moves), res.Moves)
	}
	if res.Moves[0].To != "aws_instance.charlie" {
		t.Errorf("unexpected destination %s", res.Moves[0].To)
	}
	// alpha shares a bigram with charlie and bravo does not, so alpha is the
	// better-scoring pair and takes the create.
	if res.Moves[0].From != "aws_instance.alpha" {
		t.Errorf("want the higher-scoring destroy to win, got %s", res.Moves[0].From)
	}
	if len(res.Unexplained) != 1 || res.Unexplained[0].Address != "aws_instance.bravo" {
		t.Errorf("the loser must be reported as an unexplained destroy, got %+v", res.Unexplained)
	}
}

func TestAllUnknownAttributesDoNotPair(t *testing.T) {
	res := analyze(t, "all_unknown.json", defaults())
	if len(res.Moves) != 0 {
		t.Fatalf("nothing comparable must not pass the default threshold: %+v", res.Moves)
	}
	if len(res.Unexplained) != 1 {
		t.Fatalf("want the destroy reported, got %+v", res.Unexplained)
	}
}

func TestReplaceIsReportedNotPaired(t *testing.T) {
	res := analyze(t, "mixed.json", defaults())

	for _, m := range res.Moves {
		if strings.Contains(m.From, "launch_template") || strings.Contains(m.To, "launch_template") {
			t.Errorf("a replace must never become a moved block: %s -> %s", m.From, m.To)
		}
	}
	if len(res.Replacements) != 1 || res.Replacements[0].Address != "aws_launch_template.workers" {
		t.Fatalf("want the launch template listed as a replacement, got %+v", res.Replacements)
	}
}

func TestNoOpUpdateAndDataSourcesAreIgnored(t *testing.T) {
	res := analyze(t, "mixed.json", defaults())
	all := RenderHCL(res) + RenderSummary(res)
	for _, nope := range []string{"aws_vpc.main", "aws_iam_role.app", "data.aws_ami.ubuntu"} {
		if strings.Contains(all, nope) {
			t.Errorf("%s should not appear in the report", nope)
		}
	}
}

func TestMixedPlanFindsBothRenames(t *testing.T) {
	res := analyze(t, "mixed.json", defaults())

	if len(res.Moves) != 2 {
		t.Fatalf("want 2 moved blocks, got %d: %+v", len(res.Moves), res.Moves)
	}
	if len(res.Unexplained) != 1 || res.Unexplained[0].Address != "aws_s3_bucket.legacy_logs" {
		t.Fatalf("want only the bucket unexplained, got %+v", res.Unexplained)
	}
}

func TestSameNameOnlyDropsRenames(t *testing.T) {
	res := analyze(t, "mixed.json", Options{MinConfidence: 0.6, SameNameOnly: true})

	if len(res.Moves) != 1 {
		t.Fatalf("want only the module move, got %+v", res.Moves)
	}
	if res.Moves[0].From != "module.app.aws_security_group.lb" {
		t.Errorf("unexpected move %s -> %s", res.Moves[0].From, res.Moves[0].To)
	}
}

func TestIgnoreGlobSkipsAddresses(t *testing.T) {
	res := analyze(t, "mixed.json", Options{MinConfidence: 0.6, Ignore: []string{"module.app.*", "aws_s3_bucket.*"}})

	if len(res.Moves) != 1 || res.Moves[0].From != "aws_instance.web" {
		t.Fatalf("want only the instance rename, got %+v", res.Moves)
	}
	if len(res.Unexplained) != 0 {
		t.Fatalf("the ignored bucket must not be reported, got %+v", res.Unexplained)
	}
}

func TestMinConfidenceGate(t *testing.T) {
	strict := analyze(t, "rename.json", Options{MinConfidence: 0.9})
	if len(strict.Moves) != 0 {
		t.Errorf("0.85 must not pass a 0.9 threshold, got %+v", strict.Moves)
	}
	loose := analyze(t, "low_similarity.json", Options{MinConfidence: 0.05})
	if len(loose.Moves) != 1 {
		t.Errorf("a very low threshold should let the weak pair through, got %+v", loose.Moves)
	}
}

func TestOutputIsByteStable(t *testing.T) {
	p := load(t, "mixed.json")
	first := RenderHCL(Analyze(p, defaults()))
	for i := 0; i < 20; i++ {
		if got := RenderHCL(Analyze(p, defaults())); got != first {
			t.Fatalf("run %d differs from the first run:\n%s\nvs\n%s", i, got, first)
		}
	}
}

func TestOutputIndependentOfPlanOrder(t *testing.T) {
	p := load(t, "mixed.json")
	want := RenderHCL(Analyze(p, defaults()))

	reversed := &tfplan.Plan{FormatVersion: p.FormatVersion}
	for i := len(p.ResourceChanges) - 1; i >= 0; i-- {
		reversed.ResourceChanges = append(reversed.ResourceChanges, p.ResourceChanges[i])
	}
	if got := RenderHCL(Analyze(reversed, defaults())); got != want {
		t.Fatalf("reordering resource_changes changed the output:\n%s\nvs\n%s", got, want)
	}
}

var movedBlockRE = regexp.MustCompile(`(?m)^moved \{\n  from = (\S+)\n  to   = (\S+)\n\}$`)

func TestEmittedHCLIsWellFormed(t *testing.T) {
	res := analyze(t, "mixed.json", defaults())
	hcl := RenderHCL(res)

	blocks := movedBlockRE.FindAllStringSubmatch(hcl, -1)
	if len(blocks) != len(res.Moves) {
		t.Fatalf("want %d parseable moved blocks, found %d in:\n%s", len(res.Moves), len(blocks), hcl)
	}
	for _, b := range blocks {
		for _, addr := range b[1:] {
			if strings.ContainsAny(addr, `"' `) {
				t.Errorf("address %q should be a bare reference, not a string", addr)
			}
		}
	}
	// Braces balance and every non-block line is a comment.
	if strings.Count(hcl, "{") != strings.Count(hcl, "}") {
		t.Errorf("unbalanced braces in:\n%s", hcl)
	}
	for _, line := range strings.Split(strings.TrimSpace(hcl), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "", trimmed == "}", trimmed == "moved {":
		case strings.HasPrefix(trimmed, "#"):
		case strings.HasPrefix(trimmed, "from ="), strings.HasPrefix(trimmed, "to   ="):
		default:
			t.Errorf("unexpected line in HCL output: %q", line)
		}
	}
}

func TestEmptyResultStillValidHCL(t *testing.T) {
	hcl := RenderHCL(analyze(t, "real_destroy.json", defaults()))
	if strings.Contains(hcl, "moved {") {
		t.Fatalf("no moves means no blocks, got:\n%s", hcl)
	}
	for _, line := range strings.Split(strings.TrimSpace(hcl), "\n") {
		if !strings.HasPrefix(line, "#") {
			t.Errorf("comment-only output expected, got %q", line)
		}
	}
}

func TestSummaryNamesTheRealDestroy(t *testing.T) {
	s := RenderSummary(analyze(t, "mixed.json", defaults()))
	if !strings.Contains(s, "DESTROY aws_s3_bucket.legacy_logs") {
		t.Errorf("summary should shout about the real destroy:\n%s", s)
	}
	if !strings.Contains(s, "rename  aws_instance.web -> aws_instance.api") {
		t.Errorf("summary should list the rename:\n%s", s)
	}
}

func TestNameSimilarityBounds(t *testing.T) {
	cases := []struct {
		a, b   string
		lo, hi float64
	}{
		{"lb", "lb", 1, 1},
		{"web", "api", 0, 0},
		{"web_server", "webserver", 0.5, 1},
		{"a", "c", 0, 0},
		{"", "", 1, 1},
	}
	for _, c := range cases {
		got := nameSimilarity(c.a, c.b)
		if got < c.lo || got > c.hi {
			t.Errorf("nameSimilarity(%q,%q) = %.3f, want in [%.2f,%.2f]", c.a, c.b, got, c.lo, c.hi)
		}
	}
}

func TestIsUnknownHandlesPartialStructures(t *testing.T) {
	if isUnknown(nil) || isUnknown(false) {
		t.Error("absent or false means known")
	}
	if !isUnknown(true) || !isUnknown(map[string]any{"Name": true}) || !isUnknown([]any{true}) {
		t.Error("true and nested structures mean unknown")
	}
}

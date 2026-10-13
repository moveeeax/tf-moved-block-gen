package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const examplePlan = "../../examples/plan.json"

func invoke(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestCleanPlanExitsZeroWithoutFlag(t *testing.T) {
	code, out, _ := invoke(t, "", "--plan", examplePlan)
	if code != exitOK {
		t.Fatalf("want exit 0, got %d", code)
	}
	if !strings.Contains(out, "moved {") {
		t.Errorf("expected moved blocks on stdout, got:\n%s", out)
	}
}

func TestFailOnUnexplainedExitsOne(t *testing.T) {
	code, _, errb := invoke(t, "", "--plan", examplePlan, "--fail-on-unexplained")
	if code != exitFindings {
		t.Fatalf("want exit 1 on an unexplained destroy, got %d (stderr: %s)", code, errb)
	}
	if !strings.Contains(errb, "aws_s3_bucket.legacy_logs") {
		t.Errorf("stderr should name the destroy:\n%s", errb)
	}
}

func TestFailOnUnexplainedExitsZeroWhenAllExplained(t *testing.T) {
	code, _, errb := invoke(t, "",
		"--plan", examplePlan, "--fail-on-unexplained", "--ignore", "aws_s3_bucket.*")
	if code != exitOK {
		t.Fatalf("want exit 0 once the destroy is ignored, got %d (stderr: %s)", code, errb)
	}
}

func TestBrokenJSONExitsTwo(t *testing.T) {
	if code, _, _ := invoke(t, "{not json", "--fail-on-unexplained"); code != exitError {
		t.Fatalf("want exit 2 on a parse error, got %d", code)
	}
	if code, _, _ := invoke(t, `{"format_version":"1.2"}`); code != exitError {
		t.Fatalf("want exit 2 when resource_changes is absent, got %d", code)
	}
	if code, _, _ := invoke(t, "", "--plan", "/nonexistent/plan.json"); code != exitError {
		t.Fatalf("want exit 2 on a missing file, got %d", code)
	}
	if code, _, _ := invoke(t, "", "--plan", examplePlan, "--min-confidence", "1.5"); code != exitError {
		t.Fatalf("want exit 2 on an out-of-range threshold, got %d", code)
	}
}

func TestReadsStdin(t *testing.T) {
	data, err := os.ReadFile(examplePlan)
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := invoke(t, string(data))
	if code != exitOK {
		t.Fatalf("want exit 0, got %d", code)
	}
	if !strings.Contains(out, "from = aws_instance.web") {
		t.Errorf("stdin plan not analyzed:\n%s", out)
	}
}

func TestJSONReportShape(t *testing.T) {
	code, out, _ := invoke(t, "", "--plan", examplePlan, "--json")
	if code != exitOK {
		t.Fatalf("want exit 0, got %d", code)
	}
	var report struct {
		Moved []struct {
			From       string   `json:"from"`
			To         string   `json:"to"`
			Confidence float64  `json:"confidence"`
			Matched    []string `json:"matched_attributes"`
		} `json:"moved"`
		Unexplained []struct {
			Address string `json:"address"`
		} `json:"unexplained_destroys"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out)
	}
	if len(report.Moved) != 2 {
		t.Fatalf("want 2 moves in the report, got %d", len(report.Moved))
	}
	if report.Moved[0].Confidence <= 0 || len(report.Moved[0].Matched) == 0 {
		t.Errorf("report lost the evidence: %+v", report.Moved[0])
	}
	if len(report.Unexplained) != 1 {
		t.Errorf("want 1 unexplained destroy, got %d", len(report.Unexplained))
	}
	if strings.Contains(out, "moved {") {
		t.Errorf("--json must not also print HCL:\n%s", out)
	}
}

func TestOutWritesFileAndKeepsStdoutQuiet(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "moved.tf")
	code, out, _ := invoke(t, "", "--plan", examplePlan, "--out", dst)
	if code != exitOK {
		t.Fatalf("want exit 0, got %d", code)
	}
	if out != "" {
		t.Errorf("--out should keep stdout empty, got:\n%s", out)
	}
	written, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "moved {") {
		t.Errorf("file has no moved blocks:\n%s", written)
	}
}

// The committed example output doubles as a golden file: if the pairing or the
// rendering changes, this test says so.
func TestExampleOutputMatchesGoldenFile(t *testing.T) {
	want, err := os.ReadFile("../../examples/moved.tf")
	if err != nil {
		t.Fatal(err)
	}
	_, got, _ := invoke(t, "", "--plan", examplePlan)
	if got != string(want) {
		t.Fatalf("examples/moved.tf is stale.\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestVersionFlag(t *testing.T) {
	code, out, _ := invoke(t, "", "--version")
	if code != exitOK || !strings.Contains(out, "tf-moved-block-gen") {
		t.Fatalf("unexpected --version behaviour: %d %q", code, out)
	}
}

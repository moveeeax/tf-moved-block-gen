// Command tf-moved-block-gen reads a Terraform plan in JSON form, pairs the
// destroyed addresses with the created ones, and writes the moved blocks that
// turn a rebuild back into a rename.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/moveeeax/tf-moved-block-gen/internal/moved"
	"github.com/moveeeax/tf-moved-block-gen/internal/tfplan"
)

// version is overridden at release time with -ldflags "-X main.version=...".
var version = "0.1.0"

const (
	exitOK       = 0
	exitFindings = 1
	exitError    = 2
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tf-moved-block-gen", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		planPath   = fs.String("plan", "", "plan JSON `file` from terraform show -json tfplan (default: stdin)")
		outPath    = fs.String("out", "", "write the moved blocks to this `file` instead of stdout")
		minConf    = fs.Float64("min-confidence", 0.6, "minimum confidence in [0,1] for a pair to become a moved block")
		asJSON     = fs.Bool("json", false, "print the full report as JSON on stdout")
		failUnexpl = fs.Bool("fail-on-unexplained", false, "exit 1 when a destroy has no moved block explaining it")
		sameName   = fs.Bool("same-name-only", false, "only pair addresses whose resource name is identical (module moves)")
		showVer    = fs.Bool("version", false, "print the version and exit")
		ignore     stringList
	)
	fs.Var(&ignore, "ignore", "`glob` on resource address to skip; repeatable or comma-separated")

	fs.Usage = func() {
		fmt.Fprintf(stderr, "tf-moved-block-gen - propose Terraform moved blocks from a plan\n\n")
		fmt.Fprintf(stderr, "usage: tf-moved-block-gen [flags]\n\n")
		fs.PrintDefaults()
		fmt.Fprintf(stderr, "\nexit codes: 0 clean, 1 findings with --fail-on-unexplained, 2 error\n")
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitError
	}
	if *showVer {
		fmt.Fprintf(stdout, "tf-moved-block-gen %s\n", version)
		return exitOK
	}
	if *minConf < 0 || *minConf > 1 {
		fmt.Fprintf(stderr, "error: --min-confidence must be between 0 and 1, got %v\n", *minConf)
		return exitError
	}

	src := stdin
	if *planPath != "" {
		f, err := os.Open(*planPath)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitError
		}
		defer f.Close()
		src = f
	}

	plan, err := tfplan.Load(src)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}

	res := moved.Analyze(plan, moved.Options{
		MinConfidence: *minConf,
		SameNameOnly:  *sameName,
		Ignore:        ignore,
	})

	hcl := moved.RenderHCL(res)
	if *outPath != "" {
		if err := os.WriteFile(*outPath, []byte(hcl), 0o644); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitError
		}
	}

	switch {
	case *asJSON:
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitError
		}
	case *outPath == "":
		fmt.Fprint(stdout, hcl)
	}

	if !*asJSON {
		fmt.Fprint(stderr, moved.RenderSummary(res))
	}

	if *failUnexpl && len(res.Unexplained) > 0 {
		return exitFindings
	}
	return exitOK
}

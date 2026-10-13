package tfplan

import (
	"strings"
	"testing"
)

func TestKindClassification(t *testing.T) {
	cases := []struct {
		actions []string
		want    Kind
	}{
		{[]string{"create"}, KindCreate},
		{[]string{"delete"}, KindDelete},
		{[]string{"delete", "create"}, KindReplace},
		{[]string{"create", "delete"}, KindReplace}, // create_before_destroy
		{[]string{"no-op"}, KindOther},
		{[]string{"update"}, KindOther},
		{[]string{"read"}, KindOther},
		{nil, KindOther},
		{[]string{"create", "forget"}, KindOther}, // unknown action wins
	}
	for _, c := range cases {
		if got := (Change{Actions: c.actions}).Kind(); got != c.want {
			t.Errorf("actions %v: got %q, want %q", c.actions, got, c.want)
		}
	}
}

func TestLoadParsesAddressesAndIndexes(t *testing.T) {
	const doc = `{
	  "format_version": "1.2",
	  "terraform_version": "1.9.5",
	  "resource_changes": [
	    {"address":"module.a.aws_instance.web[\"x\"]","module_address":"module.a",
	     "mode":"managed","type":"aws_instance","name":"web","index":"x",
	     "change":{"actions":["delete"],"before":{"ami":"ami-1"},"after":null}}
	  ]
	}`
	p, err := Load(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(p.ResourceChanges) != 1 {
		t.Fatalf("want 1 change, got %d", len(p.ResourceChanges))
	}
	rc := p.ResourceChanges[0]
	if rc.Address != `module.a.aws_instance.web["x"]` {
		t.Errorf("address mangled: %q", rc.Address)
	}
	if rc.Index != "x" {
		t.Errorf("for_each key lost: %#v", rc.Index)
	}
	if rc.Change.Kind() != KindDelete {
		t.Errorf("want delete, got %q", rc.Change.Kind())
	}
}

func TestLoadAcceptsEmptyPlan(t *testing.T) {
	p, err := Load(strings.NewReader(`{"format_version":"1.2","resource_changes":[]}`))
	if err != nil {
		t.Fatalf("an empty plan is not an error: %v", err)
	}
	if len(p.ResourceChanges) != 0 {
		t.Errorf("want no changes, got %d", len(p.ResourceChanges))
	}
}

func TestLoadRejectsNonPlanJSON(t *testing.T) {
	for _, doc := range []string{`{"hello":"world"}`, `{`, `[]`, ``} {
		if _, err := Load(strings.NewReader(doc)); err == nil {
			t.Errorf("want an error for %q", doc)
		}
	}
}

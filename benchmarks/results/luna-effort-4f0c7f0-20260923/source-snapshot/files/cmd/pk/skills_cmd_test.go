package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/skillinstall"
)

func TestSelectSkillCandidateRequiresExplicitChoice(t *testing.T) {
	candidates := []skillinstall.Candidate{
		{Name: "review", Path: "skills/review"},
		{Name: "testing", Path: "skills/testing"},
	}
	if _, err := selectSkillCandidate(candidates, nil); err == nil || !strings.Contains(err.Error(), "choose one") {
		t.Fatalf("missing selector error = %v", err)
	}
	got, err := selectSkillCandidate(candidates, []string{"skills/testing"})
	if err != nil || got.Name != "testing" {
		t.Fatalf("selected candidate = %#v, %v", got, err)
	}
	if _, err := selectSkillCandidate(candidates, []string{"other"}); err == nil {
		t.Fatal("unknown selector was accepted")
	}
}

func TestSkillsCLIListAndUsage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	// An empty managed directory lists successfully without contacting a source.
	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand(context.Background(), []string{"list"}, &stdout, &stderr); code != 0 || stdout.Len() != 0 {
		t.Fatalf("list: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if code := runSkillsCommand(context.Background(), nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "pk skills search") {
		t.Fatalf("usage: code=%d stderr=%q", code, stderr.String())
	}
}

func TestSkillsCLICommandDispatch(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := runMain([]string{"skills", "list"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("skills list: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

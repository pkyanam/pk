package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSelectEffortTasksIsRestrictedAndStable(t *testing.T) {
	tasks, err := selectEffortTasks("webhook, jobqueue")
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{tasks[0].name, tasks[1].name}; !reflect.DeepEqual(got, []string{"webhook", "jobqueue"}) {
		t.Fatalf("selected tasks = %v", got)
	}
	for _, selection := range []string{"", "webhook,webhook", "clamp", "webhook,"} {
		if _, err := selectEffortTasks(selection); err == nil {
			t.Errorf("selectEffortTasks(%q) succeeded", selection)
		}
	}
}

func TestEffortArmsAlternateOrderByRepetition(t *testing.T) {
	arms := []effortArm{{name: "low", effort: "low"}, {name: "medium", effort: "medium"}}
	first := effortArmsForRepetition(1, arms)
	second := effortArmsForRepetition(2, arms)
	if first[0].effort != "low" || first[1].effort != "medium" || second[0].effort != "medium" || second[1].effort != "low" {
		t.Fatalf("orders = %v then %v", first, second)
	}
	if arms[0].effort != "low" || arms[1].effort != "medium" {
		t.Fatalf("input arms mutated: %v", arms)
	}
}

func TestEffortSummaryNamesTreatmentAndAvoidsOtherPolicyClaims(t *testing.T) {
	passed := true
	path := filepath.Join(t.TempDir(), "summary.md")
	result := suite{
		Model: "gpt-6-luna", Effort: "paired:low,medium", Repetitions: 2,
		Experiment: "Luna reasoning effort: low vs medium", EffortAblationExperiment: true,
		Records: []runRecord{{Engine: "pk-effort-low", Effort: "low", Task: "webhook", Phase: "verification", CorrectnessPassed: &passed}},
	}
	if err := writeMarkdown(path, result); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"reasoning effort (low vs medium)", "full tool-output context policy", "Effort arm", "pk-effort-low (low)", "cached input is included in total input"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary missing %q:\n%s", want, text)
		}
	}
	for _, wrong := range []string{"4096 characters", "treatment changes the advertised Bash default"} {
		if strings.Contains(text, wrong) {
			t.Errorf("effort summary contains unrelated treatment claim %q:\n%s", wrong, text)
		}
	}
}

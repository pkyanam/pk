package main

import (
	"reflect"
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

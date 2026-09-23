package main

import (
	"strings"
	"testing"
)

func TestOpenTUIEnvironmentDisablesRuntimeTelemetry(t *testing.T) {
	environment := setEnvironment([]string{"PATH=/bin", "DO_NOT_TRACK=0", "HOME=/tmp/private"}, map[string]string{"DO_NOT_TRACK": "1", "PK_EXECUTABLE": "/tmp/pk"})
	values := make(map[string]string)
	for _, value := range environment {
		name, entry, ok := strings.Cut(value, "=")
		if ok {
			values[name] = entry
		}
	}
	if values["DO_NOT_TRACK"] != "1" {
		t.Fatalf("frontend DO_NOT_TRACK = %q", values["DO_NOT_TRACK"])
	}
	if values["PK_EXECUTABLE"] != "/tmp/pk" || values["HOME"] != "/tmp/private" {
		t.Fatalf("other environment values were not preserved: %#v", values)
	}
}

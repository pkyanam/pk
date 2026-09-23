package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/skillinstall"
)

type skillInstallRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn skillInstallRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func skillInstallResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestRPCSkillCandidatePayloadRoundTripsIntoInstall(t *testing.T) {
	root := t.TempDir()
	client := &http.Client{Transport: skillInstallRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch {
		case request.URL.Host == "api.github.com" && strings.HasSuffix(request.URL.Path, "/repo"):
			return skillInstallResponse(`{"default_branch":"main"}`), nil
		case request.URL.Host == "api.github.com" && strings.Contains(request.URL.Path, "/git/trees/"):
			return skillInstallResponse(`{"tree":[{"path":"skills/review/SKILL.md","type":"blob","mode":"100644"},{"path":"skills/review/reference.md","type":"blob","mode":"100644"}]}`), nil
		case request.URL.Host == "raw.githubusercontent.com" && strings.HasSuffix(request.URL.Path, "/SKILL.md"):
			return skillInstallResponse("---\nname: review\ndescription: Review code.\n---\nUse the review steps."), nil
		case request.URL.Host == "raw.githubusercontent.com" && strings.HasSuffix(request.URL.Path, "/reference.md"):
			return skillInstallResponse("Reference data."), nil
		default:
			t.Fatalf("unexpected request %s", request.URL)
			return nil, nil
		}
	})}
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{
		ctx:          context.Background(),
		output:       sink,
		diagnostics:  io.Discard,
		requestTypes: map[string]string{},
		skillManagerFactory: func() skillinstall.Manager {
			return skillinstall.Manager{Root: filepath.Join(root, "skills"), Client: client}
		},
	}
	finished := make(chan turnDone, 1)

	browsePayload, _ := json.Marshal(map[string]string{"source": "owner/repo"})
	server.handle(rpcMessage{Version: 1, ID: "browse", Type: "skill_source_list", Payload: browsePayload}, finished)
	if event := readRPCEvent(t, sink); event.Type != "skill_source_list_started" {
		t.Fatalf("browse start=%+v", event)
	}
	catalog := readRPCEvent(t, sink)
	if catalog.Type != "skill_source_candidates" {
		t.Fatalf("candidate event=%+v", catalog)
	}
	encoded, _ := json.Marshal(catalog.Payload)
	var response struct {
		Candidates []skillinstall.Candidate `json:"candidates"`
	}
	if err := json.Unmarshal(encoded, &response); err != nil || len(response.Candidates) != 1 {
		t.Fatalf("candidate payload=%s err=%v", encoded, err)
	}
	selected := response.Candidates[0]
	if selected.Source != "https://github.com/owner/repo/tree/main/skills/review" || selected.Path != "skills/review" {
		t.Fatalf("candidate payload does not preserve an installable source/path: %+v", selected)
	}

	installPayload, _ := json.Marshal(map[string]string{"source": selected.Source, "path": selected.Path})
	server.handle(rpcMessage{Version: 1, ID: "install", Type: "skill_install", Payload: installPayload}, finished)
	if event := readRPCEvent(t, sink); event.Type != "skill_install_started" {
		t.Fatalf("install start=%+v", event)
	}
	installed := readRPCEvent(t, sink)
	if installed.Type != "skill_installed" {
		t.Fatalf("install result=%+v", installed)
	}
	var installedPayload struct {
		Skill skillinstall.Installed `json:"skill"`
	}
	encoded, _ = json.Marshal(installed.Payload)
	if err := json.Unmarshal(encoded, &installedPayload); err != nil {
		t.Fatal(err)
	}
	if installedPayload.Skill.Name != "review" {
		t.Fatalf("installed skill=%+v", installedPayload.Skill)
	}
}

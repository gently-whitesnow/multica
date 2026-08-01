package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newIssueExternalCreateTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "create"}
	cmd.Flags().String("provider", "", "")
	cmd.Flags().String("instance-id", "", "")
	cmd.Flags().String("external-id", "", "")
	cmd.Flags().String("external-url", "", "")
	cmd.Flags().String("title", "", "")
	cmd.Flags().String("description", "", "")
	cmd.Flags().Bool("description-stdin", false, "")
	cmd.Flags().String("description-file", "", "")
	cmd.Flags().Bool("allow-external-file", false, "")
	cmd.Flags().String("status", "", "")
	cmd.Flags().String("priority", "", "")
	cmd.Flags().String("assignee-type", "", "")
	cmd.Flags().String("assignee-id", "", "")
	cmd.Flags().String("project-id", "", "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func TestRunIssueExternalCreateUsesProjectionContract(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/integration/issues" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer msp_test" || r.Header.Get("X-Workspace-ID") != "ws-1" {
			t.Fatalf("missing integration auth headers: %#v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"external_issue_ref": map[string]any{"provider": "overtime", "instance_id": "primary", "external_id": "task-1", "issue_id": "issue-1"},
			"issue":              map[string]any{"id": "issue-1", "identifier": "MUL-1", "status": "todo"},
			"issue_path":         "/demo/issues/MUL-1", "created": true,
		})
	}))
	defer srv.Close()
	t.Chdir(t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "msp_test")

	cmd := newIssueExternalCreateTestCmd()
	for name, value := range map[string]string{
		"provider": "overtime", "instance-id": "primary", "external-id": "task-1", "title": "Projection",
		"assignee-type": "agent", "assignee-id": "11111111-1111-1111-1111-111111111111",
	} {
		_ = cmd.Flags().Set(name, value)
	}
	out, err := captureStdout(t, func() error { return runIssueExternalCreate(cmd, nil) })
	if err != nil {
		t.Fatalf("run external create: %v", err)
	}
	if !strings.Contains(out, `"issue_path": "/demo/issues/MUL-1"`) {
		t.Fatalf("output missing stable link: %s", out)
	}
	ref := body["external_issue_ref"].(map[string]any)
	issue := body["issue"].(map[string]any)
	if ref["external_id"] != "task-1" || issue["assignee_type"] != "agent" {
		t.Fatalf("request body = %#v", body)
	}
}

func TestRunIssueExternalGetEscapesReferenceParts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/integration/issues" || r.URL.Query().Get("external_id") != "ticket/1" {
			t.Fatalf("request URL = %s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"external_issue_ref": map[string]any{"provider": "tracker", "instance_id": "primary west", "external_id": "ticket/1", "issue_id": "issue-1"},
			"issue":              map[string]any{"id": "issue-1", "identifier": "MUL-1", "status": "done"},
			"issue_url":          "https://app.example/demo/issues/MUL-1", "created": false,
		})
	}))
	defer srv.Close()
	t.Chdir(t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "msp_test")

	cmd := &cobra.Command{Use: "get"}
	cmd.Flags().String("output", "json", "")
	out, err := captureStdout(t, func() error {
		return runIssueExternalGet(cmd, []string{"tracker", "primary west", "ticket/1"})
	})
	if err != nil {
		t.Fatalf("run external get: %v", err)
	}
	if !strings.Contains(out, "https://app.example/demo/issues/MUL-1") {
		t.Fatalf("output missing issue URL: %s", out)
	}
}

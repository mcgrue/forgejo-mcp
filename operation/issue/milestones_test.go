package issue

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codeberg.org/goern/forgejo-mcp/v2/pkg/forgejo"
	forgejo_sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v2"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func listRepoMilestonesRequest(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      ListRepoMilestonesToolName,
			Arguments: args,
		},
	}
}

func setMilestoneTestClient(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	testServer := httptest.NewServer(handler)
	client, err := forgejo_sdk.NewClient(testServer.URL, forgejo_sdk.SetForgejoVersion("7.0.0"))
	if err != nil {
		testServer.Close()
		t.Fatalf("creating test client: %v", err)
	}
	forgejo.SetClientForTesting(client)
	return testServer
}

func TestListRepoMilestonesToolRegistered(t *testing.T) {
	mcpServer := server.NewMCPServer("forgejo-mcp", "test")
	RegisterTool(mcpServer)

	registered := mcpServer.GetTool(ListRepoMilestonesToolName)
	if registered == nil {
		t.Fatalf("tool %q is not registered", ListRepoMilestonesToolName)
	}
	if registered.Tool.Name != "list_repo_milestones" {
		t.Fatalf("registered tool name = %q, want %q", registered.Tool.Name, "list_repo_milestones")
	}
}

func TestListRepoMilestonesFnReturnsSelectedFieldsAndPagination(t *testing.T) {
	testServer := setMilestoneTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want %q", r.Method, http.MethodGet)
		}
		if r.URL.Path != "/api/v1/repos/test-owner/test-repo/milestones" {
			t.Errorf("path = %q, want repository milestones path", r.URL.Path)
		}
		for key, want := range map[string]string{"state": "all", "page": "2", "limit": "25"} {
			if got := r.URL.Query().Get(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", `</api/v1/repos/test-owner/test-repo/milestones?page=3&limit=25&state=all>; rel="next", </api/v1/repos/test-owner/test-repo/milestones?page=4&limit=25&state=all>; rel="last"`)
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":            42,
				"title":         "v2.0",
				"description":   "Next release",
				"state":         "open",
				"open_issues":   7,
				"closed_issues": 3,
				"due_on":        "2026-08-15T12:00:00Z",
			},
		})
	}))
	defer testServer.Close()

	result, err := ListRepoMilestonesFn(context.Background(), listRepoMilestonesRequest(map[string]any{
		"owner": "test-owner",
		"repo":  "test-repo",
		"state": "all",
		"page":  float64(2),
		"limit": float64(25),
	}))
	if err != nil {
		t.Fatalf("ListRepoMilestonesFn returned error: %v", err)
	}

	var envelope struct {
		Result listRepoMilestonesResult `json:"Result"`
	}
	if err := json.Unmarshal([]byte(resultText(t, result)), &envelope); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}
	if len(envelope.Result.Milestones) != 1 {
		t.Fatalf("milestones count = %d, want 1", len(envelope.Result.Milestones))
	}
	milestone := envelope.Result.Milestones[0]
	if milestone.ID != 42 || milestone.Title != "v2.0" || milestone.Description != "Next release" || milestone.State != "open" {
		t.Errorf("milestone = %+v, want selected SDK fields", milestone)
	}
	if milestone.OpenIssues != 7 || milestone.ClosedIssues != 3 {
		t.Errorf("issue counts = %d open, %d closed; want 7 open, 3 closed", milestone.OpenIssues, milestone.ClosedIssues)
	}
	wantDueOn := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	if milestone.DueOn == nil || !milestone.DueOn.Equal(wantDueOn) {
		t.Errorf("due_on = %v, want %v", milestone.DueOn, wantDueOn)
	}
	if envelope.Result.Page != 2 || envelope.Result.Limit != 25 || envelope.Result.NextPage != 3 || envelope.Result.LastPage != 4 {
		t.Errorf("pagination = page %d, limit %d, next %d, last %d; want 2, 25, 3, 4",
			envelope.Result.Page,
			envelope.Result.Limit,
			envelope.Result.NextPage,
			envelope.Result.LastPage,
		)
	}
}

func TestListRepoMilestonesFnUsesDefaults(t *testing.T) {
	testServer := setMilestoneTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for key, want := range map[string]string{"state": "open", "page": "1", "limit": "50"} {
			if got := r.URL.Query().Get(key); got != want {
				t.Errorf("%s = %q, want default %q", key, got, want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer testServer.Close()

	result, err := ListRepoMilestonesFn(context.Background(), listRepoMilestonesRequest(map[string]any{
		"owner": "test-owner",
		"repo":  "test-repo",
	}))
	if err != nil {
		t.Fatalf("ListRepoMilestonesFn returned error: %v", err)
	}

	var envelope struct {
		Result listRepoMilestonesResult `json:"Result"`
	}
	if err := json.Unmarshal([]byte(resultText(t, result)), &envelope); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}
	if envelope.Result.Page != 1 || envelope.Result.Limit != 50 {
		t.Errorf("pagination = page %d, limit %d; want defaults 1, 50", envelope.Result.Page, envelope.Result.Limit)
	}
}

func TestListRepoMilestonesFnRejectsInvalidArgumentsWithoutRequest(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "invalid state", args: map[string]any{"state": "merged"}},
		{name: "fractional page", args: map[string]any{"page": 1.5}},
		{name: "zero page", args: map[string]any{"page": float64(0)}},
		{name: "limit over 100", args: map[string]any{"limit": float64(101)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requestCount atomic.Int32
			testServer := setMilestoneTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("[]"))
			}))
			defer testServer.Close()

			args := map[string]any{"owner": "test-owner", "repo": "test-repo"}
			for key, value := range test.args {
				args[key] = value
			}

			result, err := ListRepoMilestonesFn(context.Background(), listRepoMilestonesRequest(args))
			if err == nil {
				t.Fatalf("expected argument error, got result %+v", result)
			}
			if got := requestCount.Load(); got != 0 {
				t.Fatalf("API request count = %d, want 0", got)
			}
		})
	}
}

func TestListRepoMilestonesFnReturnsForgejoAPIError(t *testing.T) {
	testServer := setMilestoneTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"milestones unavailable"}`, http.StatusInternalServerError)
	}))
	defer testServer.Close()

	result, err := ListRepoMilestonesFn(context.Background(), listRepoMilestonesRequest(map[string]any{
		"owner": "test-owner",
		"repo":  "test-repo",
	}))
	if err == nil {
		t.Fatalf("expected Forgejo API error, got result %+v", result)
	}
	if !strings.Contains(err.Error(), "milestones unavailable") {
		t.Errorf("error = %q, want Forgejo API error message", err)
	}
}

func TestUpdateIssueFnSetsMilestoneID(t *testing.T) {
	var requestBody []byte
	testServer := setMilestoneTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %q, want %q", r.Method, http.MethodPatch)
		}
		if r.URL.Path != "/api/v1/repos/test-owner/test-repo/issues/17" {
			t.Errorf("path = %q, want issue update path", r.URL.Path)
		}
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":17,"index":17,"title":"Test issue"}`))
	}))
	defer testServer.Close()

	result, err := UpdateIssueFn(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: UpdateIssueToolName,
			Arguments: map[string]any{
				"owner":     "test-owner",
				"repo":      "test-repo",
				"index":     float64(17),
				"milestone": "42",
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateIssueFn returned error: %v", err)
	}
	if result == nil {
		t.Fatal("expected tool result, got nil")
	}

	var body map[string]any
	if err := json.Unmarshal(requestBody, &body); err != nil {
		t.Fatalf("unmarshaling request body: %v", err)
	}
	if got := body["milestone"]; got != float64(42) {
		t.Errorf("milestone request value = %v, want numeric ID 42", got)
	}
}

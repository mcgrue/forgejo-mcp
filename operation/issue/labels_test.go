package issue

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"codeberg.org/goern/forgejo-mcp/v2/pkg/forgejo"
	forgejo_sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v2"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func listRepoLabelsRequest(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      ListRepoLabelsToolName,
			Arguments: args,
		},
	}
}

func setLabelTestClient(t *testing.T, handler http.Handler) *httptest.Server {
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

func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if result == nil {
		t.Fatal("expected a tool result, got nil")
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected one content item, got %d", len(result.Content))
	}
	textContent, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}
	return textContent.Text
}

func TestListRepoLabelsToolRegistered(t *testing.T) {
	mcpServer := server.NewMCPServer("forgejo-mcp", "test")
	RegisterTool(mcpServer)

	registered := mcpServer.GetTool(ListRepoLabelsToolName)
	if registered == nil {
		t.Fatalf("tool %q is not registered", ListRepoLabelsToolName)
	}
	if registered.Tool.Name != "list_repo_labels" {
		t.Fatalf("registered tool name = %q, want %q", registered.Tool.Name, "list_repo_labels")
	}
}

func TestListRepoLabelsFnReturnsSelectedFieldsAndPagination(t *testing.T) {
	testServer := setLabelTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want %q", r.Method, http.MethodGet)
		}
		if r.URL.Path != "/api/v1/repos/test-owner/test-repo/labels" {
			t.Errorf("path = %q, want repository labels path", r.URL.Path)
		}
		if got := r.URL.Query().Get("page"); got != "2" {
			t.Errorf("page = %q, want %q", got, "2")
		}
		if got := r.URL.Query().Get("limit"); got != "25" {
			t.Errorf("limit = %q, want %q", got, "25")
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", `</api/v1/repos/test-owner/test-repo/labels?page=3&limit=25>; rel="next", </api/v1/repos/test-owner/test-repo/labels?page=5&limit=25>; rel="last"`)
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":          123,
				"name":        "Kind/Bug",
				"color":       "d73a4a",
				"description": "Something is not working",
				"url":         "https://tracker.invalid/api/v1/labels/123",
			},
		})
	}))
	defer testServer.Close()

	result, err := ListRepoLabelsFn(context.Background(), listRepoLabelsRequest(map[string]any{
		"owner": "test-owner",
		"repo":  "test-repo",
		"page":  float64(2),
		"limit": float64(25),
	}))
	if err != nil {
		t.Fatalf("ListRepoLabelsFn returned error: %v", err)
	}

	text := resultText(t, result)
	if strings.Contains(text, "tracker.invalid") || strings.Contains(text, `"url"`) {
		t.Fatalf("result exposes SDK label URL: %s", text)
	}

	var envelope struct {
		Result listRepoLabelsResult `json:"Result"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}
	if len(envelope.Result.Labels) != 1 {
		t.Fatalf("labels count = %d, want 1", len(envelope.Result.Labels))
	}
	label := envelope.Result.Labels[0]
	if label.ID != 123 || label.Name != "Kind/Bug" || label.Color != "d73a4a" || label.Description != "Something is not working" {
		t.Errorf("label = %+v, want selected SDK fields", label)
	}
	if envelope.Result.Page != 2 || envelope.Result.Limit != 25 || envelope.Result.NextPage != 3 || envelope.Result.LastPage != 5 {
		t.Errorf("pagination = page %d, limit %d, next %d, last %d; want 2, 25, 3, 5",
			envelope.Result.Page,
			envelope.Result.Limit,
			envelope.Result.NextPage,
			envelope.Result.LastPage,
		)
	}
}

func TestListRepoLabelsFnUsesDefaultPagination(t *testing.T) {
	testServer := setLabelTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("page"); got != "1" {
			t.Errorf("page = %q, want default %q", got, "1")
		}
		if got := r.URL.Query().Get("limit"); got != "50" {
			t.Errorf("limit = %q, want default %q", got, "50")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer testServer.Close()

	result, err := ListRepoLabelsFn(context.Background(), listRepoLabelsRequest(map[string]any{
		"owner": "test-owner",
		"repo":  "test-repo",
	}))
	if err != nil {
		t.Fatalf("ListRepoLabelsFn returned error: %v", err)
	}

	var envelope struct {
		Result listRepoLabelsResult `json:"Result"`
	}
	if err := json.Unmarshal([]byte(resultText(t, result)), &envelope); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}
	if envelope.Result.Page != 1 || envelope.Result.Limit != 50 {
		t.Errorf("pagination = page %d, limit %d; want defaults 1, 50", envelope.Result.Page, envelope.Result.Limit)
	}
}

func TestListRepoLabelsFnRejectsInvalidPaginationWithoutRequest(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "invalid page type", args: map[string]any{"page": "one"}},
		{name: "fractional page", args: map[string]any{"page": 1.5}},
		{name: "fractional limit", args: map[string]any{"limit": 1.5}},
		{name: "zero page", args: map[string]any{"page": float64(0)}},
		{name: "zero limit", args: map[string]any{"limit": float64(0)}},
		{name: "negative page", args: map[string]any{"page": float64(-1)}},
		{name: "negative limit", args: map[string]any{"limit": float64(-1)}},
		{name: "limit over 100", args: map[string]any{"limit": float64(101)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requestCount atomic.Int32
			testServer := setLabelTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("[]"))
			}))
			defer testServer.Close()

			args := map[string]any{
				"owner": "test-owner",
				"repo":  "test-repo",
			}
			for key, value := range test.args {
				args[key] = value
			}

			result, err := ListRepoLabelsFn(context.Background(), listRepoLabelsRequest(args))
			if err == nil {
				t.Fatalf("expected pagination error, got result %+v", result)
			}
			if got := requestCount.Load(); got != 0 {
				t.Fatalf("API request count = %d, want 0", got)
			}
		})
	}
}

func TestListRepoLabelsFnReturnsForgejoAPIError(t *testing.T) {
	var requestCount atomic.Int32
	testServer := setLabelTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		http.Error(w, `{"message":"labels unavailable"}`, http.StatusInternalServerError)
	}))
	defer testServer.Close()

	result, err := ListRepoLabelsFn(context.Background(), listRepoLabelsRequest(map[string]any{
		"owner": "test-owner",
		"repo":  "test-repo",
	}))
	if err == nil {
		t.Fatalf("expected Forgejo API error, got result %+v", result)
	}
	if !strings.Contains(err.Error(), "labels unavailable") {
		t.Errorf("error = %q, want Forgejo API error message", err)
	}
	if got := requestCount.Load(); got != 1 {
		t.Errorf("API request count = %d, want 1", got)
	}
}

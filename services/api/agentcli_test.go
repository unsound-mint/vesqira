package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
	"github.com/unsound-mint/vesqira/services/api/internal/server"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// The agent drafts and the owner publishes (ADR 0036). This holds structurally: the MCP server offers no tool that
// publishes, sends a reply, resolves a thread, makes or revokes a reader link, or changes the publish server. A new
// tool is a deliberate change to this list.
func TestTheAgentCanDraftButNeverPublishSendOrResolve(t *testing.T) {
	base := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	bs, _ := boards.Open(cat.DB(), base)
	rp, _ := reports.Open(cat.DB(), base)
	api := httptest.NewServer((&server.Server{Store: cat, Boards: bs, Reports: rp}).Handler())
	defer api.Close()
	c, err := newAPIClient(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newAPIClient("http://192.0.2.1:8787"); err == nil {
		t.Error("a non-loopback API address was accepted")
	}

	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = mcpServer(c).Run(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	want := []string{"attach_image", "create_report", "draft_reply", "get_run", "list_comments", "list_figures", "list_reports", "list_runs", "read_report", "update_report"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools changed:\n got %v\nwant %v", names, want)
	}
	for _, n := range names {
		for _, banned := range []string{"publish", "send", "resolve", "reader", "revoke", "settings"} {
			if strings.Contains(n, banned) {
				t.Errorf("tool %q looks like it does what only the owner may do", n)
			}
		}
	}

	// The read tools answer (a shape mismatch with the API would surface here as an error result).
	for _, name := range []string{"list_reports", "list_figures", "list_comments"} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err != nil || res.IsError {
			t.Errorf("%s: %v %+v", name, err, res)
		}
	}

	// A write through the agent is recorded as the agent's, and a stale base is refused with the current text.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "create_report", Arguments: map[string]any{"title": "Weekly notes 2026-W46", "body": "draft"}})
	if err != nil || res.IsError {
		t.Fatalf("create: %v %+v", err, res)
	}
	var rep reports.Report
	_ = json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &rep)
	if rep.LastAuthor != reports.AuthorAgent {
		t.Fatalf("author %q", rep.LastAuthor)
	}
	_, _ = rp.Update(workspace.Local, "local-owner", rep.ID, reports.Update{Body: ptr("owner text"), Base: rep.Revision, Author: reports.AuthorOwner})
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "update_report", Arguments: map[string]any{"id": rep.ID, "base": rep.Revision, "body": "agent text"}})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !res.IsError || !strings.Contains(text, "409") || !strings.Contains(text, "owner text") {
		t.Fatalf("stale update should be a conflict showing the current text: %s", text)
	}
}

func ptr(s string) *string { return &s }

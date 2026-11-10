package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/unsound-mint/vesqira/services/api/internal/notionimport"
)

// The agent's way in (ADR 0030): `mcp` is a stdio MCP server and `report` / `import` are plain commands. All of them
// are clients of the running owner API on loopback, so the app stays the only writer of the catalog and every write
// is recorded with its author. None of them can publish, send a reply or resolve a thread: those routes are not
// called from here.

const instructions = `Vesqira holds the researcher's reports: Markdown documents for a supervisor, drafted by you and published by the researcher.
You can read reports, runs, figures and the supervisor's comments, create and update report drafts, attach images and draft replies to comments. You cannot publish, send a reply or resolve a thread; the researcher does that after reading your draft.
Report syntax: CommonMark with GitHub tables and $…$ / $$…$$ math. A line holding only ![[figure:<revision>]] embeds a pinned figure (use list_figures for revisions); a line holding only [[run:<id>]] embeds a run summary; [[Report title]] links another report; ![alt](attachment:<sha256>) shows an image added with attach_image.
A report may start with front matter: a line "---", then "date: YYYY-MM-DD" (the date the report is about) and "summary: <one sentence>" (shown under the title), then "---". Only date and summary are shown or published; keep any other keys you find as they are.
Reports form a tree of pages: list_reports gives each one's parent id; create a new report under the right page (for example a meeting report under the page that holds the meeting reports).
Scientific rules: never invent units, calibration or coordinate frames; say "unknown" when a value is not known. Keep measured results, interpretation and hypotheses apart and label interpretation as such. VGT orientations are axial (0° = 180°): never average angles arithmetically. A run finishing is not evidence that its result is valid.
Every update names the revision you read (base); if someone saved in between you get a conflict with the current text: read it again and reapply your change rather than overwriting.`

type apiClient struct {
	base string
	http *http.Client
}

func newAPIClient(raw string) (*apiClient, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return nil, fmt.Errorf("--api must be the app's loopback address, like http://127.0.0.1:8787")
	}
	if ip := net.ParseIP(u.Hostname()); u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("--api must be a loopback address: the owner API is never reached over the network")
	}
	return &apiClient{base: strings.TrimRight(raw, "/"), http: &http.Client{Timeout: 60 * time.Second}}, nil
}

func (c *apiClient) call(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Vesqira is not answering at %s (is the app running?): %v", c.base, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error   string          `json:"error"`
			Current json.RawMessage `json:"current"`
		}
		_ = json.Unmarshal(b, &e)
		msg := e.Error
		if msg == "" {
			msg = strings.TrimSpace(string(b))
		}
		if len(e.Current) > 0 {
			msg += "\nCurrent report:\n" + string(e.Current)
		}
		return fmt.Errorf("%d: %s", resp.StatusCode, msg)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func textResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}

func errResult(err error) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil, nil
}

type noArgs struct{}

type idArgs struct {
	ID int64 `json:"id" jsonschema:"the id"`
}

type createArgs struct {
	Title  string `json:"title" jsonschema:"report title, unique, without [ ] | or #"`
	Body   string `json:"body" jsonschema:"Markdown body"`
	Parent int64  `json:"parent,omitempty" jsonschema:"id of the page to create it under (optional; 0 is the top level)"`
}

type updateArgs struct {
	ID    int64   `json:"id" jsonschema:"report id"`
	Base  int     `json:"base" jsonschema:"the revision you read; a stale base is refused with the current text"`
	Title *string `json:"title,omitempty" jsonschema:"new title (optional; links to the report are rewritten)"`
	Body  *string `json:"body,omitempty" jsonschema:"new full Markdown body (optional)"`
}

type attachArgs struct {
	Path string `json:"path" jsonschema:"a PNG, JPEG or GIF file on this computer (at most 8 MiB, 4096x4096)"`
}

type commentsArgs struct {
	Report string `json:"report,omitempty" jsonschema:"report key to filter by (optional)"`
}

type draftArgs struct {
	CommentID int64  `json:"commentId" jsonschema:"the comment or thread to answer"`
	Body      string `json:"body" jsonschema:"the reply; the researcher reads and sends it"`
}

func readImage(path string) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif":
	default:
		return "", errors.New("attachments are PNG, JPEG or GIF files")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 8<<20+1))
	if err != nil {
		return "", err
	}
	if len(b) > 8<<20 {
		return "", errors.New("the image is larger than 8 MiB")
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func mcpServer(c *apiClient) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "vesqira", Title: "Vesqira reports", Version: "0.1"}, &mcp.ServerOptions{Instructions: instructions})
	mcp.AddTool(s, &mcp.Tool{Name: "list_reports", Description: "List reports: id, key, title, parent page id, date, summary, revision, last author, whether published."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			var out any
			if err := c.call("GET", "/api/reports", nil, &out); err != nil {
				return errResult(err)
			}
			return textResult(out)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "read_report", Description: "Read a report: body, revision, backlinks, publications, the supervisor's comments and drafted replies, and [[links]] that name no report."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a idArgs) (*mcp.CallToolResult, any, error) {
			var out any
			if err := c.call("GET", "/api/reports/"+strconv.FormatInt(a.ID, 10), nil, &out); err != nil {
				return errResult(err)
			}
			return textResult(out)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "create_report", Description: "Create a report draft. It is not shared with anyone until the researcher publishes it."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a createArgs) (*mcp.CallToolResult, any, error) {
			var out any
			if err := c.call("POST", "/api/reports", map[string]any{"title": a.Title, "body": a.Body, "author": "agent", "parent": a.Parent}, &out); err != nil {
				return errResult(err)
			}
			return textResult(out)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "update_report", Description: "Save a new revision of a report draft (full body). Name the revision you read as base."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a updateArgs) (*mcp.CallToolResult, any, error) {
			var out any
			if err := c.call("POST", "/api/reports/"+strconv.FormatInt(a.ID, 10), map[string]any{"title": a.Title, "body": a.Body, "base": a.Base, "author": "agent"}, &out); err != nil {
				return errResult(err)
			}
			return textResult(out)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "list_runs", Description: "List imported runs with their settings, declared results (value, unit, uncertainty) and the researcher's assessment."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			var out any
			if err := c.call("GET", "/api/runs", nil, &out); err != nil {
				return errResult(err)
			}
			return textResult(out)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_run", Description: "One run in full: settings, results, outputs, assessment."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a idArgs) (*mcp.CallToolResult, any, error) {
			var out any
			if err := c.call("GET", "/api/runs/"+strconv.FormatInt(a.ID, 10), nil, &out); err != nil {
				return errResult(err)
			}
			return textResult(out)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "list_figures", Description: "List pinned figures: id, title, caption, revision (embed with ![[figure:<revision>]] on its own line) and what they show."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			var out struct {
				Figures []struct {
					ID         int64           `json:"id"`
					Title      string          `json:"title"`
					Caption    string          `json:"caption"`
					Revision   string          `json:"revision"`
					CreatedAt  time.Time       `json:"createdAt"`
					Provenance json.RawMessage `json:"provenance"`
				} `json:"figures"`
			}
			if err := c.call("GET", "/api/figures", nil, &out); err != nil {
				return errResult(err)
			}
			return textResult(out.Figures)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "attach_image", Description: "Upload an image file (for example a plot you made) and get the Markdown that shows it in a report."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a attachArgs) (*mcp.CallToolResult, any, error) {
			data, err := readImage(a.Path)
			if err != nil {
				return errResult(err)
			}
			var out any
			if err := c.call("POST", "/api/attachments", map[string]string{"data": data}, &out); err != nil {
				return errResult(err)
			}
			return textResult(out)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "list_comments", Description: "The supervisor's comments on published reports (synced from the publish server) and replies waiting for the researcher to send."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a commentsArgs) (*mcp.CallToolResult, any, error) {
			var out any
			if err := c.call("GET", "/api/report-comments?report="+url.QueryEscape(a.Report), nil, &out); err != nil {
				return errResult(err)
			}
			return textResult(out)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "draft_reply", Description: "Draft a reply to a comment thread. The researcher reads, edits and sends it; nothing reaches the supervisor from here."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a draftArgs) (*mcp.CallToolResult, any, error) {
			var out any
			if err := c.call("POST", "/api/report-comments/"+strconv.FormatInt(a.CommentID, 10)+"/draft", map[string]any{"body": a.Body, "author": "agent"}, &out); err != nil {
				return errResult(err)
			}
			return textResult(out)
		})
	return s
}

const defaultAPI = "http://127.0.0.1:8787"

// agentCommand handles `mcp`, `report` and `import`; it returns false for anything else.
func agentCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "mcp":
		fs := flag.NewFlagSet("mcp", flag.ExitOnError)
		api := fs.String("api", defaultAPI, "the running app's loopback address")
		_ = fs.Parse(args[1:])
		c, err := newAPIClient(*api)
		if err != nil {
			fatal(err)
		}
		if err := mcpServer(c).Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			fatal(err)
		}
	case "report":
		reportCommand(args[1:])
	case "import":
		if len(args) < 2 || args[1] != "notion" {
			fmt.Fprintln(os.Stderr, "usage: vesqira import notion [--dry-run] [--api URL] EXPORT.zip|FOLDER")
			os.Exit(2)
		}
		importNotion(args[2:])
	default:
		return false
	}
	return true
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func reportCommand(args []string) {
	usage := func() {
		fmt.Fprintln(os.Stderr, `usage:
  vesqira report list
  vesqira report get ID
  vesqira report new --title TITLE [--file BODY.md]      (body from stdin without --file)
  vesqira report put ID --base REV [--title TITLE] [--file BODY.md]
Writes are recorded as the agent's unless --as owner.`)
		os.Exit(2)
	}
	if len(args) == 0 {
		usage()
	}
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	api := fs.String("api", defaultAPI, "the running app's loopback address")
	title := fs.String("title", "", "title")
	file := fs.String("file", "", "Markdown body file (- or empty reads stdin for new)")
	base := fs.Int("base", 0, "the revision you read (put)")
	as := fs.String("as", "agent", "author recorded: agent or owner")
	verb, rest := args[0], args[1:]
	id := int64(0)
	if verb == "get" || verb == "put" {
		if len(rest) == 0 {
			usage()
		}
		var err error
		if id, err = strconv.ParseInt(rest[0], 10, 64); err != nil {
			usage()
		}
		rest = rest[1:]
	}
	_ = fs.Parse(rest)
	c, err := newAPIClient(*api)
	if err != nil {
		fatal(err)
	}
	readBody := func(required bool) *string {
		if *file == "" && !required {
			return nil
		}
		var b []byte
		if *file == "" || *file == "-" {
			b, err = io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		} else {
			b, err = os.ReadFile(*file)
		}
		if err != nil {
			fatal(err)
		}
		s := string(b)
		return &s
	}
	var out any
	switch verb {
	case "list":
		err = c.call("GET", "/api/reports", nil, &out)
	case "get":
		err = c.call("GET", "/api/reports/"+strconv.FormatInt(id, 10), nil, &out)
	case "new":
		err = c.call("POST", "/api/reports", map[string]any{"title": *title, "body": *readBody(true), "author": *as}, &out)
	case "put":
		in := map[string]any{"base": *base, "author": *as, "body": readBody(false)}
		if *title != "" {
			in["title"] = *title
		}
		err = c.call("POST", "/api/reports/"+strconv.FormatInt(id, 10), in, &out)
	default:
		usage()
	}
	if err != nil {
		fatal(err)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}

// openExport opens a Notion export: the zip Notion produces, or a folder it was unpacked into. A folder is packed
// into an in-memory zip (stored, not compressed) so both go through the same checks; symbolic links are skipped and
// the folder may hold at most 2 GiB.
func openExport(path string) (io.ReaderAt, int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	if !st.IsDir() {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, 0, err
		}
		return bytes.NewReader(b), int64(len(b)), nil
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var total int64
	err = filepath.WalkDir(path, func(p string, d iofs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&iofs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if total += info.Size(); total > 2<<30 {
			return errors.New("the export folder holds more than 2 GiB")
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: filepath.ToSlash(rel), Method: zip.Store})
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	if err := zw.Close(); err != nil {
		return nil, 0, err
	}
	return bytes.NewReader(buf.Bytes()), int64(buf.Len()), nil
}

// importNotion reads a Notion "Markdown & CSV" export and creates draft reports through the running app. Nothing is
// published. A title already in use gets " (Notion)" appended; links between imported pages follow the rename.
func importNotion(args []string) {
	fs := flag.NewFlagSet("import notion", flag.ExitOnError)
	api := fs.String("api", defaultAPI, "the running app's loopback address")
	dry := fs.Bool("dry-run", false, "list what would be created and change nothing")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: vesqira import notion [--dry-run] [--api URL] EXPORT.zip|FOLDER")
		os.Exit(2)
	}
	src, size, err := openExport(fs.Arg(0))
	if err != nil {
		fatal(err)
	}
	res, err := notionimport.Import(src, size, notionimport.Options{})
	if err != nil {
		fatal(fmt.Errorf("the export cannot be imported: %v", err))
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	if *dry {
		for _, p := range res.Pages {
			fmt.Printf("would create %q  (from %s, %d characters)\n", p.Title, p.Source, len(p.Markdown))
		}
		fmt.Printf("%d reports and %d images; nothing was changed\n", len(res.Pages), len(res.Images))
		return
	}
	c, err := newAPIClient(*api)
	if err != nil {
		fatal(err)
	}
	for _, im := range res.Images {
		var out struct {
			SHA string `json:"sha"`
		}
		if err := c.call("POST", "/api/attachments", map[string]string{"data": base64.StdEncoding.EncodeToString(im.Data)}, &out); err != nil {
			fatal(fmt.Errorf("image %s: %v", im.Name, err))
		}
		if out.SHA != im.SHA256 {
			fatal(fmt.Errorf("image %s was stored under a different hash; stopping", im.Name))
		}
	}
	var existing []struct {
		Title string `json:"title"`
	}
	if err := c.call("GET", "/api/reports", nil, &existing); err != nil {
		fatal(err)
	}
	taken := map[string]bool{}
	for _, r := range existing {
		taken[strings.ToLower(r.Title)] = true
	}
	renamed := map[string]string{}
	for _, p := range res.Pages {
		t := p.Title
		for i := 0; taken[strings.ToLower(t)]; i++ {
			t = p.Title + " (Notion)"
			if i > 0 {
				t = fmt.Sprintf("%s (Notion %d)", p.Title, i+1)
			}
		}
		taken[strings.ToLower(t)] = true
		if t != p.Title {
			renamed[strings.ToLower(p.Title)] = t
		}
	}
	// Parents before children, so each page is created under the page it sat under in Notion.
	ids := map[string]int64{}
	done := map[string]bool{}
	created := 0
	for pass := 0; pass < len(res.Pages)+1 && len(done) < len(res.Pages); pass++ {
		for _, p := range res.Pages {
			if done[p.Source] {
				continue
			}
			parent := int64(0)
			if p.Parent != "" {
				id, ok := ids[p.Parent]
				if !ok && !done[p.Parent] && pass < len(res.Pages) {
					continue // its parent comes first
				}
				parent = id
			}
			done[p.Source] = true
			body := p.Markdown
			for old, nu := range renamed { // follow renames in links between imported pages, labelled or not
				body = regexp.MustCompile(`(?i)\[\[`+regexp.QuoteMeta(old)+`(\|[^\]]*)?\]\]`).ReplaceAllString(body, "[["+strings.ReplaceAll(nu, "$", "$$")+"$1]]")
			}
			t := p.Title
			if nu, ok := renamed[strings.ToLower(t)]; ok {
				t = nu
			}
			var out struct {
				ID int64 `json:"id"`
			}
			if err := c.call("POST", "/api/reports", map[string]any{"title": t, "body": body, "author": "owner", "parent": parent}, &out); err != nil {
				fmt.Fprintf(os.Stderr, "could not create %q: %v\n", t, err)
				continue
			}
			ids[p.Source] = out.ID
			created++
		}
	}
	fmt.Printf("created %d of %d reports as drafts (%d images); nothing was published\n", created, len(res.Pages), len(res.Images))
}

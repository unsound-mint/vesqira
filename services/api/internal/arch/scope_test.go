package arch

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ADR 0050 section 4, guard 5, made mechanical for slice 3. Every table of the catalog is classified below, and the schema
// snapshot must agree: a scoped table has a workspace_id column, a child names the key of a scoped parent. Then every SQL text in
// the program (every string literal that holds SQL, in every internal package but the migrations) is read:
//
//   - a statement on a scoped table names workspace_id, so a list cannot return another workspace's rows and a lookup by id
//     cannot reach one (a foreign id answers "not found", which the server turns into 404);
//   - a statement on a child table names workspace_id or the key of its parent, which the caller has already looked up in its
//     workspace;
//   - a function that holds a statement on a scoped table takes a workspace.ID parameter, so the scope comes from the caller
//     (in the end, the request's actor), never from a constant or a guess;
//   - a table not classified fails the test: a new table is a decision about scope.
//
// The exceptions are explicit: capability lookups by a token hash (review links, invitations), and the tables of features the
// group server never mounts (localOnly; internal/server TestLocalOnlyFeaturesStayUnmounted pins their permissions and routes as
// unmounted).

// scopedTables carry workspace_id and are reached by requests on the group server.
var scopedTables = set("datasets", "dataset_locations", "figures", "reports", "runs", "run_views", "projects", "attachments",
	"boards", "snapshots", "saved_regions", "masks", "subsets", "exploration_steps", "notifications", "audit", "member_comments",
	"invitations", "workspace_roots", "dataset_access",
	// slice 4: what linked instances pushed (ADR 0066)
	"pushed_items", "pushed_assets",
	// slice 5: relayed run requests and the key holders' inboxes (ADR 0066 section 3)
	"relay_inboxes", "run_requests")

// localOnlyTables carry workspace_id (local rows: workspace 1) but belong to features the group server does not mount (hosts,
// jobs, sweeps, transfers, offline copies: no ssh credential on the server, ADR 0050 owner answer 3; the publish server: no
// per-workspace publisher key yet). Their queries are not scoped until those features are; mounting one fails
// internal/server TestLocalOnlyFeaturesStayUnmounted first. One mounted route reads one of them: the runs list asks the jobs store
// for job lineages and keeps only those of the workspace's own runs (an empty table on a server that never ran jobs).
var localOnlyTables = set("hosts", "jobs", "sweeps", "sweep_jobs", "transfers", "offline_entries", "offline_settings",
	"publish_comments", "reply_drafts", "report_publications", "publish_settings",
	// the local side of the link to a group server (ADR 0066): its routes are local.only, so never mounted there
	"group_link", "push_queue", "push_lineage",
	// the key holder's side of relayed run requests (ADR 0066 section 3): its inbox, label-to-host map and request mirror
	"relay_local", "relayed_requests")

// childTables hold no workspace_id: each row hangs off one row of a scoped table, named by these columns, and is reached only
// through it.
var childTables = map[string][]string{
	"interpretations": {"dataset_id"}, "dataset_units": {"dataset_id"}, "dataset_nature": {"dataset_id"},
	"catalog_declarations": {"dataset_id"}, "dataset_corrections": {"dataset_id"}, "derived": {"dataset_id"},
	"board_items": {"board_id"}, "snapshot_outcomes": {"snapshot_id"}, "comments": {"snapshot_id"},
	"report_history": {"report_id"}, "report_links": {"report_id"}, "report_seen": {"report_id"},
	"run_outputs": {"run_id"}, "run_review": {"run_id"}, "run_review_history": {"run_id"}, "rohsa_runs": {"run_id"},
	"subset_versions": {"subset_id"}, "project_entries": {"project_id"}, "project_answers": {"project_id"},
	"import_rules": {"project_id"}, "import_lists": {"project_id"}, "import_applied": {"project_id"},
	"archived_entries": {"entity_id"}, "notification_reads": {"notification_id"},
	"pushed_item_assets": {"item_id"}, "pushed_references": {"item_id"}, "pushed_history": {"item_id"},
	"run_request_events": {"request_id"},
}

// globalTables are server-wide: accounts and their credentials, the workspaces themselves, memberships (read by account to
// find an actor's workspaces), and SQLite's own bookkeeping.
var globalTables = set("accounts", "identities", "password_credentials", "totp_credentials", "recovery_codes", "sessions",
	"access_tokens", "setup_tokens", "workspaces", "memberships", "sqlite_master", "sqlite_sequence")

// skippedPackages hold no request-time SQL on the catalog: the migrations and the snapshot helper, this test, and the publish
// server, which is another program with its own database (ADR 0036) and no workspace.
var skippedPackages = set("schema", "fullschema", "arch", "publish")

func set(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

var (
	sqlVerb  = regexp.MustCompile(`\b(SELECT|INSERT|UPDATE|DELETE)\b`)
	tableRef = regexp.MustCompile(`\b(?:FROM|JOIN|UPDATE|INTO)\s+([a-z_][a-z0-9_]*)`)
)

// snapshotColumns reads the newest schema snapshot: table name -> its CREATE text.
func snapshotTables(t *testing.T) map[string]string {
	t.Helper()
	files, _ := filepath.Glob("../../testdata/schema_v*.sql")
	newest, best := "", -1
	for _, f := range files {
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "schema_v"), ".sql"))
		if err == nil && n > best {
			newest, best = f, n
		}
	}
	fh, err := os.Open(newest)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	name := ""
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "-- table "); ok {
			name = rest
			continue
		}
		if name != "" && strings.HasPrefix(line, "CREATE TABLE") {
			out[name] = line
			name = ""
		}
	}
	return out
}

// Guard 5, part 1: the classification covers every table and agrees with the schema.
func TestEveryUserDataTableIsScoped(t *testing.T) {
	tables := snapshotTables(t)
	if len(tables) < 50 {
		t.Fatalf("read only %d tables from the snapshot", len(tables))
	}
	for name, create := range tables {
		hasWS := strings.Contains(create, "workspace_id INTEGER")
		switch {
		case scopedTables[name], localOnlyTables[name] && name != "sweep_jobs" && name != "offline_settings" && name != "publish_settings" && name != "report_publications" && name != "group_link" && name != "relay_local":
			if !hasWS {
				t.Errorf("%s is classified scoped but has no workspace_id column", name)
			}
		case childTables[name] != nil:
			for _, k := range childTables[name] {
				if !strings.Contains(create, " "+k+" ") && !strings.Contains(create, "("+k+" ") {
					t.Errorf("child table %s has no %s column naming its parent", name, k)
				}
			}
		case globalTables[name], localOnlyTables[name]:
		default:
			t.Errorf("table %s is not classified: decide whether it is scoped (workspace_id), a child of a scoped table, local-only or global (internal/arch/scope_test.go)", name)
		}
	}
}

type sqlSite struct {
	file, fn string
	line     int
	text     string
	wsParam  bool // the enclosing function takes a workspace.ID
}

func takesWorkspace(ft *ast.FuncType) bool {
	if ft == nil || ft.Params == nil {
		return false
	}
	for _, f := range ft.Params.List {
		if se, ok := f.Type.(*ast.SelectorExpr); ok && se.Sel.Name == "ID" {
			if x, ok := se.X.(*ast.Ident); ok && x.Name == "workspace" {
				return true
			}
		}
	}
	return false
}

// sqlSites lists every string literal holding SQL in the internal packages, with the function around it. A function literal
// inside a function counts as part of it, and may itself take the workspace.
func sqlSites(t *testing.T) []sqlSite {
	t.Helper()
	dirs, err := filepath.Glob("../*")
	if err != nil {
		t.Fatal(err)
	}
	var out []sqlSite
	fset := token.NewFileSet()
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || skippedPackages[filepath.Base(dir)] {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			rel := filepath.Join(filepath.Base(dir), filepath.Base(f))
			var visit func(n ast.Node, fn string, ws bool)
			visit = func(n ast.Node, fn string, ws bool) {
				ast.Inspect(n, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.FuncLit:
						visit(n.Body, fn, ws || takesWorkspace(n.Type))
						return false
					case *ast.BinaryExpr, *ast.BasicLit:
						// a concatenation ("SELECT " + cols + " FROM datasets WHERE ...") is one statement: its literal pieces are
						// read together, so a scope in one piece covers a table named in another
						s, ok := literalText(n)
						if !ok {
							return true
						}
						if looksLikeSQL(s) {
							out = append(out, sqlSite{file: rel, fn: fn, line: fset.Position(n.Pos()).Line, text: s, wsParam: ws})
						}
						return false
					}
					return true
				})
			}
			for _, d := range af.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok {
					if fd.Body != nil {
						visit(fd.Body, fd.Name.Name, takesWorkspace(fd.Type))
					}
				} else {
					visit(d, "", false)
				}
			}
		}
	}
	return out
}

// dynamicName stands for a table name built at run time (an operand of a concatenation, or %s in a format string).
const dynamicName = "dynamic_table_name"

var (
	formatTable = regexp.MustCompile(`\b(FROM|JOIN|INTO|UPDATE)\s+%[sv]`)
	clauseWord  = regexp.MustCompile(`\b(WHERE|ON|SELECT|SET|ORDER BY|GROUP BY|HAVING|VALUES|FROM|JOIN|RETURNING|LIMIT)\b`)
)

// inFilter reports whether the text at position i sits in a WHERE or JOIN ON clause: the nearest clause keyword before it is
// WHERE or ON (AND and OR continue a clause). A comparison in ORDER BY, a SELECT list or an UPDATE's SET does not filter.
func inFilter(q string, i int) bool {
	words := clauseWord.FindAllStringIndex(q[:i], -1)
	if len(words) == 0 {
		return false
	}
	last := q[words[len(words)-1][0]:words[len(words)-1][1]]
	return last == "WHERE" || last == "ON"
}

// filtersOn reports whether the statement compares column (optionally qualified) with equality or IN inside a WHERE or ON
// clause, on either side of the comparison.
func filtersOn(q, column string) bool {
	left := regexp.MustCompile(`\b(\w+\.)?` + column + `\s*(=|IN\s*\()`)
	right := regexp.MustCompile(`=\s*(\w+\.)?` + column + `\b`)
	for _, re := range []*regexp.Regexp{left, right} {
		for _, m := range re.FindAllStringIndex(q, -1) {
			if inFilter(q, m[0]) {
				return true
			}
		}
	}
	return false
}

// namesWorkspace: the statement filters on workspace_id by equality (or IN) in a WHERE or ON clause, or is an INSERT that sets
// it. Selecting the column, ordering by it, setting it in an UPDATE or comparing it with anything else (workspace_id>0) does not
// scope a statement.
func namesWorkspace(q string) bool {
	return filtersOn(q, "workspace_id") || (strings.Contains(q, "INSERT") && strings.Contains(q, "workspace_id"))
}

// crossWorkspace are the statements that read across workspaces on purpose, each with its reason. Keep it short.
var crossWorkspace = map[string]bool{
	// a figure's preview picture is stored once by content: deleting one figure keeps the file while any figure, in any
	// workspace, still shows it (the count is never returned to the caller)
	"lifecycle/lifecycle.go Delete": true,
	// the daily push limit is the account's across every workspace it pushes to (ADR 0066 limits); only counts are read
	"pushed/pushed.go checkDaily": true,
}

// literalText joins the string literals of a literal or a concatenation; other operands (constants, variables, calls) stand
// as a space. It reports false for anything that is not a string expression.
func literalText(n ast.Node) (string, bool) {
	switch n := n.(type) {
	case *ast.BasicLit:
		if n.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(n.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if n.Op != token.ADD {
			return "", false
		}
		l, lok := literalText(n.X)
		r, rok := literalText(n.Y)
		if !lok && !rok {
			return "", false
		}
		// an operand that is not a literal stands as a marker: after FROM, JOIN, INTO or UPDATE it is a table name the scan
		// cannot read, which fails as an unclassified table
		if !lok {
			l = dynamicName
		}
		if !rok {
			r = dynamicName
		}
		return l + " " + r, true
	case *ast.ParenExpr:
		return literalText(n.X)
	}
	return "", false
}

// looksLikeSQL: a statement verb, or a fragment that names a classified table after FROM, JOIN, UPDATE or INTO.
func looksLikeSQL(s string) bool {
	if sqlVerb.MatchString(s) {
		return true
	}
	for _, m := range tableRef.FindAllStringSubmatch(s, -1) {
		if t := m[1]; scopedTables[t] || childTables[t] != nil {
			return true
		}
	}
	return false
}

// Guard 5, part 2: every statement on a scoped table names the workspace, and its function takes one; every statement on a
// child table names the workspace or its parent's key; no statement names an unclassified table.
func TestEveryScopedQueryNamesTheWorkspace(t *testing.T) {
	sites := sqlSites(t)
	if len(sites) < 200 {
		t.Fatalf("found only %d SQL texts: the scan is broken", len(sites))
	}
	var problems []string
	for _, s := range sites {
		capability := strings.Contains(s.text, "token_hash")
		text := formatTable.ReplaceAllString(s.text, "$1 "+dynamicName)
		for _, m := range tableRef.FindAllStringSubmatch(text, -1) {
			tbl := m[1]
			where := s.file + ":" + strconv.Itoa(s.line) + " (" + s.fn + ")"
			switch {
			case strings.HasPrefix(tbl, "pragma_") || tbl == "excluded" || tbl == "json_each" || globalTables[tbl] || localOnlyTables[tbl]:
			case scopedTables[tbl]:
				if capability {
					continue
				}
				if !namesWorkspace(s.text) && !crossWorkspace[s.file+" "+s.fn] {
					problems = append(problems, where+": a statement on "+tbl+" does not filter by workspace_id = ? (nor sets it in an INSERT)")
				} else if !s.wsParam {
					problems = append(problems, where+": a statement on "+tbl+" is in a function that takes no workspace.ID")
				}
			case childTables[tbl] != nil:
				ok := namesWorkspace(s.text) || strings.HasPrefix(strings.TrimSpace(s.text), "INSERT")
				for _, k := range childTables[tbl] {
					ok = ok || filtersOn(s.text, k)
				}
				if !ok {
					problems = append(problems, where+": a statement on child table "+tbl+" names neither workspace_id nor "+strings.Join(childTables[tbl], "/"))
				}
			default:
				problems = append(problems, where+": table "+tbl+" is not classified in internal/arch/scope_test.go")
			}
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// The rule itself: a filter counts only in WHERE or ON, a child's parent key only as a comparison there, and a table name built
// at run time is unreadable, so it fails as unclassified.
func TestTheScopeRuleReadsClauses(t *testing.T) {
	for q, want := range map[string]bool{
		`SELECT id FROM boards WHERE id=? AND workspace_id=?`:                           true,
		`SELECT b.id FROM boards b JOIN memberships m ON m.workspace_id=b.workspace_id`: true,
		`INSERT INTO boards(title,workspace_id) VALUES(?,?)`:                            true,
		`SELECT workspace_id FROM boards WHERE id=? ORDER BY workspace_id=? DESC`:       false,
		`UPDATE boards SET workspace_id=? WHERE id=?`:                                   false,
		`SELECT id FROM boards WHERE workspace_id>0`:                                    false,
		`SELECT workspace_id=? FROM boards WHERE id=?`:                                  false,
	} {
		if namesWorkspace(q) != want {
			t.Errorf("namesWorkspace(%q) = %v, want %v", q, !want, want)
		}
	}
	if filtersOn(`SELECT dataset_id FROM derived WHERE op=?`, "dataset_id") || !filtersOn(`SELECT r.op FROM derived r JOIN datasets d ON d.id=r.dataset_id`, "dataset_id") {
		t.Error("a parent key in the SELECT list counted as a filter, or a join on it did not")
	}
	if got := formatTable.ReplaceAllString(`SELECT * FROM %s WHERE id=?`, "$1 "+dynamicName); !strings.Contains(got, dynamicName) {
		t.Errorf("a format-string table name escaped: %s", got)
	}
}

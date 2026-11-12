package relay

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
)

const (
	testLabel = "Cluster A"
	shaA      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	inst1     = "11111111111111111111111111111111"
	inst2     = "22222222222222222222222222222222"
)

func f64(v float64) *float64 { return &v }

// vgtKeys are the settings of the vgt template a request may change (output_dir and input_file are paths and never settable).
var vgtKeys = []string{"noise", "noise_mode", "noise_tile_size", "noise_sigma_multiplier", "dn", "ker", "min_valid"}

func settableKeys(template string) []string {
	if template == TemplateROHSA {
		return jobs.ROHSAEditableKeys()
	}
	return vgtKeys
}

// injections are values that must never reach a rendered script or config line through a request.
var injections = []string{"/etc/passwd", "../x", "$(id)", "`id`", "a;b", "a'b", "a\"b", "a\nb", "a\\b"}

// openInbox accepts every settable key of both templates with no min, max or choices: the weakest bound an inbox can publish.
func openInbox() Inbox {
	lim := Limits{MaxCPUs: 16, MaxMemGiB: 64, MaxTimeMinutes: 600}
	var ts []TemplateBounds
	for _, tpl := range []string{TemplateVGT, TemplateROHSA} {
		tb := TemplateBounds{Template: tpl, Limits: lim}
		for _, k := range settableKeys(tpl) {
			tb.Keys = append(tb.Keys, Bound{Key: k})
		}
		ts = append(ts, tb)
	}
	return Inbox{Labels: []Label{{Label: testLabel, Templates: ts}}}
}

// vgtInbox bounds dn to 1..8, noise_mode to two choices and leaves ker and min_valid free.
func vgtInbox() Inbox {
	return Inbox{Labels: []Label{{Label: testLabel, Templates: []TemplateBounds{{
		Template: TemplateVGT,
		Keys: []Bound{
			{Key: "dn", Min: f64(1), Max: f64(8)},
			{Key: "noise_mode", Choices: []string{"scalar", "channel"}},
			{Key: "ker"}, {Key: "min_valid"},
		},
		Limits: Limits{MaxCPUs: 16, MaxMemGiB: 64, MaxTimeMinutes: 600},
	}}}}}
}

func vgtAsk(params map[string]string) Ask {
	return Ask{Label: testLabel, Template: TemplateVGT, Params: params}
}

func mustRefuse(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: accepted, want a refusal", what)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("%s: error %v does not wrap ErrInvalid", what, err)
	}
}

func TestCheckAskAcceptsAWellFormedRequest(t *testing.T) {
	a := vgtAsk(map[string]string{"dn": "4", "noise_mode": "scalar"})
	a.Resources = Resources{CPUs: 16, MemGiB: 64, TimeMinutes: 600}
	if err := CheckAsk(vgtInbox(), a); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAskRefusesKeysTheInboxOrTheWhitelistDoNotAllow(t *testing.T) {
	in := vgtInbox()
	// a key the template knows but this inbox does not list, one no template knows, one of the other template
	for _, k := range []string{"noise", "foo", "n_gauss"} {
		mustRefuse(t, CheckAsk(in, vgtAsk(map[string]string{k: "1"})), "key "+k)
	}
	// forbidden and path keys are refused even when a hostile or old inbox lists them
	for _, tpl := range []string{TemplateVGT, TemplateROHSA} {
		for _, k := range []string{"output_dir", "input_file", "input"} {
			in := Inbox{Labels: []Label{{Label: testLabel, Templates: []TemplateBounds{{Template: tpl, Keys: []Bound{{Key: k}}, Limits: Limits{MaxCPUs: 1}}}}}}
			a := Ask{Label: testLabel, Template: tpl, Params: map[string]string{k: "/etc/passwd"}}
			mustRefuse(t, CheckAsk(in, a), tpl+" "+k)
			g := Ask{Label: testLabel, Template: tpl, Grid: []GridKey{{Key: k, Values: []string{"/etc/passwd"}}}}
			mustRefuse(t, CheckAsk(in, g), tpl+" grid "+k)
			mustRefuse(t, CheckAskWhitelist(g), tpl+" whitelist grid "+k)
			mustRefuse(t, CheckAskWhitelist(a), tpl+" whitelist "+k)
		}
	}
	// the template and label must exist in the inbox
	mustRefuse(t, CheckAsk(in, Ask{Label: testLabel, Template: "custom"}), "custom template")
	mustRefuse(t, CheckAsk(in, Ask{Label: testLabel, Template: TemplateROHSA}), "template the label does not take")
	mustRefuse(t, CheckAsk(in, Ask{Label: "Other", Template: TemplateVGT}), "unknown label")
}

func TestCheckAskRefusesValuesOutsideTheInboxBound(t *testing.T) {
	in := vgtInbox()
	for name, p := range map[string]map[string]string{
		"below min":       {"dn": "0"},
		"above max":       {"dn": "9"},
		"not a number":    {"dn": "x"},
		"not a choice":    {"noise_mode": "auto"},
		"another choice":  {"noise_mode": "spatial"},
		"template reject": {"dn": "-1"},
	} {
		mustRefuse(t, CheckAsk(in, vgtAsk(p)), name)
	}
	g := vgtAsk(nil)
	g.Grid = []GridKey{{Key: "dn", Values: []string{"2", "9"}}}
	mustRefuse(t, CheckAsk(in, g), "grid value above max")
	// the bound edges are inclusive
	for _, v := range []string{"1", "8"} {
		if err := CheckAsk(in, vgtAsk(map[string]string{"dn": v})); err != nil {
			t.Fatalf("dn=%s: %v", v, err)
		}
	}
}

func TestCheckAskGridLimits(t *testing.T) {
	in := openInbox()
	four := []string{"1", "2", "3", "4"}
	ok := vgtAsk(nil)
	ok.Grid = []GridKey{{Key: "dn", Values: four}, {Key: "ker", Values: four}, {Key: "min_valid", Values: four}}
	if err := CheckAsk(in, ok); err != nil {
		t.Fatalf("64 combinations: %v", err)
	}
	big := vgtAsk(nil)
	big.Grid = []GridKey{{Key: "dn", Values: four}, {Key: "ker", Values: four}, {Key: "min_valid", Values: []string{"1", "2", "3", "4", "5"}}}
	mustRefuse(t, CheckAsk(in, big), "80 combinations")
	var v65 []string
	for i := 1; i <= 65; i++ {
		v65 = append(v65, strconv.Itoa(i))
	}
	one := vgtAsk(nil)
	one.Grid = []GridKey{{Key: "dn", Values: v65}}
	mustRefuse(t, CheckAsk(in, one), "65 values")
	fourKeys := vgtAsk(nil)
	fourKeys.Grid = []GridKey{{Key: "dn", Values: []string{"1"}}, {Key: "ker", Values: []string{"1"}}, {Key: "min_valid", Values: []string{"1"}}, {Key: "noise", Values: []string{"1"}}}
	mustRefuse(t, CheckAsk(in, fourKeys), "four grid keys")
	twice := vgtAsk(nil)
	twice.Grid = []GridKey{{Key: "dn", Values: []string{"1"}}, {Key: "dn", Values: []string{"2"}}}
	mustRefuse(t, CheckAsk(in, twice), "key twice in grid")
	both := vgtAsk(map[string]string{"dn": "3"})
	both.Grid = []GridKey{{Key: "dn", Values: []string{"1", "2"}}}
	mustRefuse(t, CheckAsk(in, both), "key in params and grid")
	empty := vgtAsk(nil)
	empty.Grid = []GridKey{{Key: "dn"}}
	mustRefuse(t, CheckAsk(in, empty), "empty grid values")
	dup := vgtAsk(nil)
	dup.Grid = []GridKey{{Key: "dn", Values: []string{"1", "1"}}}
	mustRefuse(t, CheckAsk(in, dup), "repeated grid value")
}

func TestCheckAskRefusesResourcesAboveTheInboxLimits(t *testing.T) {
	in := vgtInbox()
	for name, r := range map[string]Resources{
		"cpus":     {CPUs: 17},
		"mem":      {MemGiB: 65},
		"time":     {TimeMinutes: 601},
		"negative": {CPUs: -1},
	} {
		a := vgtAsk(nil)
		a.Resources = r
		mustRefuse(t, CheckAsk(in, a), name)
	}
	// a limit of zero means the request may not choose that resource at all
	zero := vgtInbox()
	zero.Labels[0].Templates[0].Limits = Limits{}
	a := vgtAsk(nil)
	a.Resources = Resources{CPUs: 1}
	mustRefuse(t, CheckAsk(zero, a), "cpus under a zero limit")
}

func TestCheckAskRefusesAnUnsafeNote(t *testing.T) {
	a := vgtAsk(nil)
	a.Note = "ok\x00"
	mustRefuse(t, CheckAsk(vgtInbox(), a), "control character")
	a.Note = strings.Repeat("x", MaxNote+1)
	mustRefuse(t, CheckAsk(vgtInbox(), a), "long note")
	a.Note = "line one\n\tline two"
	if err := CheckAsk(vgtInbox(), a); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAskRefusesOverlappingROHSASettings(t *testing.T) {
	a := Ask{Label: testLabel, Template: TemplateROHSA, Params: map[string]string{"lambda": "1", "lambda_amp": "2"}}
	mustRefuse(t, CheckAsk(openInbox(), a), "lambda with lambda_amp")
}

// Every settable key of both templates, every injection: with an inbox that adds no bound of its own, the template's own
// validator is the only thing between the value and a rendered script, so CheckAsk must agree with it exactly.
func TestInjectionSweepThroughCheckAsk(t *testing.T) {
	in := openInbox()
	for _, tpl := range []string{TemplateVGT, TemplateROHSA} {
		keys := settableKeys(tpl)
		if len(keys) < 7 {
			t.Fatalf("%s: only %d settable keys; the sweep lost its input", tpl, len(keys))
		}
		for _, k := range keys {
			if !Settable(tpl, k) {
				t.Errorf("%s %s is listed as settable but Settable says no", tpl, k)
			}
			for _, v := range injections {
				tplErr := jobs.CheckTemplateSetting(tpl, k, v)
				p := Ask{Label: testLabel, Template: tpl, Params: map[string]string{k: v}}
				g := Ask{Label: testLabel, Template: tpl, Grid: []GridKey{{Key: k, Values: []string{v}}}}
				for name, a := range map[string]Ask{"param": p, "grid": g} {
					err := CheckAsk(in, a)
					if tplErr == nil {
						t.Logf("REPORT: the %s template validator allows %s=%q; CheckAsk %s result: %v", tpl, k, v, name, err)
						continue
					}
					if err == nil {
						t.Errorf("%s %s=%q accepted as a %s", tpl, k, v, name)
					} else if !errors.Is(err, ErrInvalid) {
						t.Errorf("%s %s=%q: %v does not wrap ErrInvalid", tpl, k, v, err)
					}
					if CheckAskWhitelist(a) == nil {
						t.Errorf("%s %s=%q accepted by the whitelist check as a %s", tpl, k, v, name)
					}
				}
			}
		}
	}
}

func TestInjectionSweepForbiddenKeysAreNeverSettable(t *testing.T) {
	for _, tpl := range []string{TemplateVGT, TemplateROHSA} {
		for _, k := range []string{"output_dir", "input_file", "input"} {
			if Settable(tpl, k) {
				t.Errorf("%s %s is settable by a request", tpl, k)
			}
		}
	}
}

func TestCheckAskWhitelistIgnoresInboxBoundsOnly(t *testing.T) {
	a := vgtAsk(map[string]string{"dn": "100", "noise_mode": "spatial"})
	mustRefuse(t, CheckAsk(vgtInbox(), a), "outside the inbox bound")
	if err := CheckAskWhitelist(a); err != nil {
		t.Fatalf("a lowered bound is the key holder's judgement: %v", err)
	}
	for name, bad := range map[string]Ask{
		"non-whitelisted key": vgtAsk(map[string]string{"output_dir": "/tmp/x"}),
		"unknown key":         vgtAsk(map[string]string{"foo": "1"}),
		"bad label":           {Label: "a.b", Template: TemplateVGT},
		"custom template":     {Label: testLabel, Template: "custom"},
		"cpus over absolute":  {Label: testLabel, Template: TemplateVGT, Resources: Resources{CPUs: MaxCPUs + 1}},
		"template rejects":    vgtAsk(map[string]string{"dn": "-5"}),
	} {
		mustRefuse(t, CheckAskWhitelist(bad), name)
	}
	g := vgtAsk(nil)
	for _, k := range []string{"dn", "ker", "min_valid", "noise_tile_size", "noise"} {
		g.Grid = append(g.Grid, GridKey{Key: k, Values: []string{"1"}})
	}
	mustRefuse(t, CheckAskWhitelist(g), "five grid keys")
}

func validClaim() Claim {
	return Claim{Instance: inst1, ScriptHash: shaA, Params: map[string]string{"dn": "4"}, Label: testLabel}
}

func TestCheckClaim(t *testing.T) {
	single := vgtAsk(nil)
	if err := CheckClaim(single, validClaim()); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*Claim){
		"short sha":         func(c *Claim) { c.ScriptHash = shaA[:63] },
		"uppercase sha":     func(c *Claim) { c.ScriptHash = strings.ToUpper(shaA) },
		"non-hex sha":       func(c *Claim) { c.ScriptHash = strings.Repeat("g", 64) },
		"no sha":            func(c *Claim) { c.ScriptHash = "" },
		"sweep hash on one": func(c *Claim) { c.SweepHash = shaB },
		"kept on one":       func(c *Claim) { c.Kept = []Combo{{"dn": "1"}} },
		"no instance":       func(c *Claim) { c.Instance = "" },
		"bad instance":      func(c *Claim) { c.Instance = "xyz" },
		"bad label":         func(c *Claim) { c.Label = "a/b" },
		"forbidden param":   func(c *Claim) { c.Params = map[string]string{"output_dir": "/tmp"} },
		"injected param":    func(c *Claim) { c.Params = map[string]string{"dn": "1;id"} },
		"cpus over cap":     func(c *Claim) { c.Resources.CPUs = MaxCPUs + 1 },
		"negative edits":    func(c *Claim) { c.EditedLines = -1 },
	} {
		c := validClaim()
		mod(&c)
		mustRefuse(t, CheckClaim(single, c), name)
	}
}

func TestCheckClaimSweep(t *testing.T) {
	sweep := vgtAsk(nil)
	sweep.Grid = []GridKey{{Key: "dn", Values: []string{"1", "2"}}, {Key: "ker", Values: []string{"3", "4"}}}
	good := Claim{Instance: inst1, SweepHash: shaB, Kept: []Combo{{"dn": "1", "ker": "3"}}, Concurrency: 1, Params: map[string]string{}, Label: testLabel}
	if err := CheckClaim(sweep, good); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{-1, 0, 17, 1000} {
		c := good
		c.Concurrency = n
		mustRefuse(t, CheckClaim(sweep, c), "concurrency "+strconv.Itoa(n))
	}
	for _, n := range []int{1, 16} {
		c := good
		c.Concurrency = n
		if err := CheckClaim(sweep, c); err != nil {
			t.Fatalf("concurrency %d: %v", n, err)
		}
	}
	for name, mod := range map[string]func(*Claim){
		"script hash on a sweep": func(c *Claim) { c.ScriptHash = shaA },
		"no sweep hash":          func(c *Claim) { c.SweepHash = "" },
		"nothing kept":           func(c *Claim) { c.Kept = nil },
		"kept outside the grid":  func(c *Claim) { c.Kept = []Combo{{"dn": "9", "ker": "3"}} },
		"kept missing a key":     func(c *Claim) { c.Kept = []Combo{{"dn": "1"}} },
		"kept twice":             func(c *Claim) { c.Kept = []Combo{{"dn": "1", "ker": "3"}, {"ker": "3", "dn": "1"}} },
	} {
		c := good
		mod(&c)
		mustRefuse(t, CheckClaim(sweep, c), name)
	}
}

func TestCheckClaimInjectionSweep(t *testing.T) {
	for _, tpl := range []string{TemplateVGT, TemplateROHSA} {
		ask := Ask{Label: testLabel, Template: tpl}
		for _, k := range settableKeys(tpl) {
			for _, v := range injections {
				if jobs.CheckTemplateSetting(tpl, k, v) == nil {
					continue
				}
				c := validClaim()
				c.Params = map[string]string{k: v}
				if CheckClaim(ask, c) == nil {
					t.Errorf("%s claim param %s=%q accepted", tpl, k, v)
				}
			}
		}
	}
}

func TestCheckReport(t *testing.T) {
	good := Report{Instance: inst1, Seq: 1, State: StateRunning}
	if err := CheckReport(good); err != nil {
		t.Fatal(err)
	}
	ok := Report{Instance: inst1, Seq: 2, State: StateFailed, Code: CodeTimeout, Exit: "0:15", RunHash: shaA, Counts: map[string]int{"draft": 1, "running": 64}}
	if err := CheckReport(ok); err != nil {
		t.Fatal(err)
	}
	nine := map[string]int{"approved": 1, "submitted": 1, "running": 1, "finished": 1, "failed": 1, "unknown": 1, "draft": 1, "a": 1, "b": 1}
	for name, mod := range map[string]func(*Report){
		"seq 0":               func(r *Report) { r.Seq = 0 },
		"negative seq":        func(r *Report) { r.Seq = -3 },
		"no instance":         func(r *Report) { r.Instance = "" },
		"bad instance":        func(r *Report) { r.Instance = strings.ToUpper(inst1) + "f" },
		"declined state":      func(r *Report) { r.State = StateDeclined },
		"requested state":     func(r *Report) { r.State = StateRequested },
		"expired state":       func(r *Report) { r.State = StateExpired },
		"empty state":         func(r *Report) { r.State = "" },
		"free text state":     func(r *Report) { r.State = "running; rm -rf /" },
		"unknown code":        func(r *Report) { r.Code = "Submitted batch job 12" },
		"exit with text":      func(r *Report) { r.Exit = "exit 0" },
		"exit three parts":    func(r *Report) { r.Exit = "0:0:0" },
		"exit too long":       func(r *Report) { r.Exit = "1000:0" },
		"exit trailing nl":    func(r *Report) { r.Exit = "0:0\n" },
		"bad run hash":        func(r *Report) { r.RunHash = "abc" },
		"counts key":          func(r *Report) { r.Counts = map[string]int{"bogus": 1} },
		"counts negative":     func(r *Report) { r.Counts = map[string]int{"running": -1} },
		"counts above combos": func(r *Report) { r.Counts = map[string]int{"running": MaxCombos + 1} },
		"nine counts":         func(r *Report) { r.Counts = nine },
	} {
		r := good
		mod(&r)
		mustRefuse(t, CheckReport(r), name)
	}
}

func TestCheckInbox(t *testing.T) {
	if err := CheckInbox(openInbox()); err != nil {
		t.Fatal(err)
	}
	if err := CheckInbox(vgtInbox()); err != nil {
		t.Fatal(err)
	}
	mk := func(mod func(*Label)) Inbox {
		in := vgtInbox()
		mod(&in.Labels[0])
		return in
	}
	badLabels := []string{"", strings.Repeat("a", 41), "a.b", "a@b", "host.example.org", " lead", "trail ", "x/y", "a\n", "a_b", "café", "a;b"}
	for _, l := range badLabels {
		mustRefuse(t, CheckInbox(mk(func(x *Label) { x.Label = l })), "label "+strconv.Quote(l))
	}
	for _, l := range []string{"a", strings.Repeat("a", 40), "Cluster-2 b"} {
		if err := CheckInbox(mk(func(x *Label) { x.Label = l })); err != nil {
			t.Fatalf("label %q: %v", l, err)
		}
	}
	tb := func(x *Label) *TemplateBounds { return &x.Templates[0] }
	var many []string
	for i := 0; i < MaxValuesPerKey+1; i++ {
		many = append(many, "1")
	}
	for name, in := range map[string]Inbox{
		"no templates":        mk(func(x *Label) { x.Templates = nil }),
		"custom template":     mk(func(x *Label) { tb(x).Template = "custom" }),
		"same template twice": mk(func(x *Label) { x.Templates = append(x.Templates, x.Templates[0]) }),
		"three templates":     mk(func(x *Label) { x.Templates = append(x.Templates, x.Templates[0], x.Templates[0]) }),
		"cpus over cap":       mk(func(x *Label) { tb(x).Limits.MaxCPUs = MaxCPUs + 1 }),
		"negative mem":        mk(func(x *Label) { tb(x).Limits.MaxMemGiB = -1 }),
		"mem over cap":        mk(func(x *Label) { tb(x).Limits.MaxMemGiB = MaxMemGiB + 1 }),
		"time over cap":       mk(func(x *Label) { tb(x).Limits.MaxTimeMinutes = MaxTimeMinutes + 1 }),
		"output_dir key":      mk(func(x *Label) { tb(x).Keys = append(tb(x).Keys, Bound{Key: "output_dir"}) }),
		"input_file key":      mk(func(x *Label) { tb(x).Keys = append(tb(x).Keys, Bound{Key: "input_file"}) }),
		"unknown key":         mk(func(x *Label) { tb(x).Keys = append(tb(x).Keys, Bound{Key: "foo"}) }),
		"rohsa key under vgt": mk(func(x *Label) { tb(x).Keys = append(tb(x).Keys, Bound{Key: "n_gauss"}) }),
		"key twice":           mk(func(x *Label) { tb(x).Keys = append(tb(x).Keys, Bound{Key: "dn"}) }),
		"min above max":       mk(func(x *Label) { tb(x).Keys = []Bound{{Key: "dn", Min: f64(5), Max: f64(1)}} }),
		"nan min":             mk(func(x *Label) { tb(x).Keys = []Bound{{Key: "dn", Min: f64(math.NaN())}} }),
		"inf max":             mk(func(x *Label) { tb(x).Keys = []Bound{{Key: "dn", Max: f64(math.Inf(1))}} }),
		"injected choice":     mk(func(x *Label) { tb(x).Keys = []Bound{{Key: "noise_mode", Choices: []string{"scalar", "a;b"}}} }),
		"65 choices":          mk(func(x *Label) { tb(x).Keys = []Bound{{Key: "dn", Choices: many}} }),
	} {
		mustRefuse(t, CheckInbox(in), name)
	}
	// duplicate labels (ignoring case) and more than eight labels
	dup := vgtInbox()
	l2 := dup.Labels[0]
	l2.Label = strings.ToUpper(l2.Label)
	dup.Labels = append(dup.Labels, l2)
	mustRefuse(t, CheckInbox(dup), "label twice")
	var nine Inbox
	for i := 0; i < MaxLabels+1; i++ {
		l := vgtInbox().Labels[0]
		l.Label = "L" + strconv.Itoa(i)
		nine.Labels = append(nine.Labels, l)
	}
	mustRefuse(t, CheckInbox(nine), "nine labels")
}

func TestInjectionSweepThroughInboxChoices(t *testing.T) {
	for _, tpl := range []string{TemplateVGT, TemplateROHSA} {
		for _, k := range settableKeys(tpl) {
			for _, v := range injections {
				if jobs.CheckTemplateSetting(tpl, k, v) == nil {
					continue
				}
				in := Inbox{Labels: []Label{{Label: testLabel, Templates: []TemplateBounds{{Template: tpl, Keys: []Bound{{Key: k, Choices: []string{v}}}}}}}}
				if CheckInbox(in) == nil {
					t.Errorf("%s %s: choice %q accepted into an inbox", tpl, k, v)
				}
			}
		}
	}
}

func TestLabelSafeKeepsHostNamesOffTheServer(t *testing.T) {
	if err := LabelSafe("Cluster A", []string{"user@login.example.org", "login"}); err != nil {
		t.Fatal(err)
	}
	mustRefuse(t, LabelSafe("my login box", []string{"login"}), "host name inside label")
	mustRefuse(t, LabelSafe("a.b", nil), "dotted label")
}

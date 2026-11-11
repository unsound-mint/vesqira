package jobs

import (
	"math"
	"strings"
	"testing"
)

func TestParseDurationAndSize(t *testing.T) {
	for in, want := range map[string]float64{"06:12:03": 22323, "3-00:00:00": 259200, "00:00.250": 0.25, "01:02:03.5": 3723.5, "1-02:03:04": 93784} {
		if got, ok := parseDuration(in); !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("parseDuration(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "UNLIMITED", "Partition_Limit", "12", "a:b:c", "-1:00:00", "1:2:3:4", "00:-5:00", strings.Repeat("9", 40)} {
		if _, ok := parseDuration(in); ok {
			t.Errorf("parseDuration(%q) accepted", in)
		}
	}
	for in, want := range map[string]int64{"1024K": 1 << 20, "81234M": 81234 << 20, "79G": 79 << 30, "1.5G": 3 << 29, "2T": 2 << 40} {
		if got, _, _, ok := parseSize(in, 1); !ok || got != want {
			t.Errorf("parseSize(%q) = %d %v, want %d", in, got, ok, want)
		}
	}
	if got, _, _, ok := parseSize("500", 1<<10); !ok || got != 500<<10 {
		t.Errorf("a bare MaxRSS is KiB: %d %v", got, ok)
	}
	if _, c, n, ok := parseSize("4000Mc", 1<<20); !ok || !c || n {
		t.Errorf("per-CPU suffix: %v %v %v", c, n, ok)
	}
	for _, in := range []string{"", "G", "-5G", "12Q", "99999999999T", "nan"} {
		if _, _, _, ok := parseSize(in, 1); ok {
			t.Errorf("parseSize(%q) accepted", in)
		}
	}
}

// sacct rows for a job: JobID,State,ExitCode,Elapsed,Start,End,Timelimit,ReqMem,ReqTRES,AllocCPUS,TotalCPU,MaxRSS,NNodes
func row(s string) []string { return strings.Split(s, "|") }

func TestUsageTakesTheLargestStepPeakAndTheJobRowTotals(t *testing.T) {
	// the job row has no MaxRSS; the .batch and numbered steps do (K, M and G are all seen): the peak is the largest of them
	u := ParseUsage([][]string{
		row("901|COMPLETED|0:0|06:12:03|s|e|3-00:00:00|140G|billing=32,cpu=32,mem=140G,node=1|32|2-03:12:00||1"),
		row("901.batch|COMPLETED|0:0|06:12:03|s|e||||32|2-03:12:00|82837504K|1"),
		row("901.extern|COMPLETED|0:0|06:12:03|s|e||||32|00:00:00|1024K|1"),
		row("901.0|COMPLETED|0:0|06:11:00|s|e||||32|00:00:10|60000M|1"),
	})
	if u == nil || u.State != "COMPLETED" || u.ElapsedSeconds != 22323 {
		t.Fatalf("%+v", u)
	}
	if u.MaxRSSBytes == nil || *u.MaxRSSBytes != 82837504<<10 {
		t.Errorf("peak %v: the largest step wins (82837504K ~ 79 GiB)", u.MaxRSSBytes)
	}
	if u.ReqMemBytes == nil || *u.ReqMemBytes != 140<<30 || u.TimelimitSec == nil || *u.TimelimitSec != 259200 || u.AllocCPUS == nil || *u.AllocCPUS != 32 {
		t.Errorf("request: %+v", u)
	}
	// 2-03:12:00 = 184320 s of CPU over 22323 s x 32 CPUs
	if u.CPUEfficiency == nil || math.Abs(*u.CPUEfficiency-184320.0/(22323*32)) > 1e-9 || u.CPUNotAccounted {
		t.Errorf("efficiency %v not-accounted %v", u.CPUEfficiency, u.CPUNotAccounted)
	}
	if u.OOMKilled {
		t.Error("not an OOM")
	}
}

func TestUsageUnitsOfMaxRSS(t *testing.T) {
	for _, c := range []struct {
		rss  string
		want int64
	}{{"2048K", 2 << 20}, {"512M", 512 << 20}, {"3G", 3 << 30}, {"4096", 4096 << 10}} {
		u := ParseUsage([][]string{row("1|COMPLETED|0:0|00:10:00|s|e|01:00:00|8G||2|00:05:00||1"), row("1.batch|COMPLETED|0:0|00:10:00|s|e||||2|00:05:00|" + c.rss + "|1")})
		if u.MaxRSSBytes == nil || *u.MaxRSSBytes != c.want {
			t.Errorf("MaxRSS %q: %v, want %d", c.rss, u.MaxRSSBytes, c.want)
		}
	}
}

func TestUsageNeverTurnsMissingIntoZero(t *testing.T) {
	// accounting gathered no memory (blank or 0 MaxRSS on every step) and no CPU columns
	u := ParseUsage([][]string{row("2|COMPLETED|0:0|00:10:00|s|e|UNLIMITED|||||"), row("2.batch|COMPLETED|0:0|00:10:00|s|e|||||0|")})
	if u == nil {
		t.Fatal("no usage")
	}
	if u.MaxRSSBytes != nil || u.ReqMemBytes != nil || u.TimelimitSec != nil || u.AllocCPUS != nil || u.TotalCPUSec != nil || u.CPUEfficiency != nil || u.CPUNotAccounted {
		t.Errorf("unreported values must be absent, not zero: %+v", u)
	}
	if ParseUsage(nil) != nil || ParseUsage([][]string{row("3.batch|COMPLETED|0:0|00:10:00")}) != nil {
		t.Error("a job with no row of its own has no usage")
	}
}

func TestUsageCPUTimeNotAccountedByTheScheduler(t *testing.T) {
	// the owner's ROHSA case: hours of wall time, TotalCPU 0 because the work runs in a process Slurm does not track
	u := ParseUsage([][]string{
		row("77|COMPLETED|0:0|06:12:03|s|e|3-00:00:00|140G||32|00:00:00||1"),
		row("77.batch|COMPLETED|0:0|06:12:03|s|e||||32|00:00:00|82837504K|1"),
	})
	if !u.CPUNotAccounted || u.CPUEfficiency != nil {
		t.Errorf("TotalCPU 0 over 6 h must be 'not accounted', not 0 %%: %+v", u)
	}
	// a very short job is not called unaccounted: there is too little to tell
	u = ParseUsage([][]string{row("78|COMPLETED|0:0|00:00:20|s|e|00:10:00|1G||4|00:00:00||1")})
	if u.CPUNotAccounted || u.CPUEfficiency == nil || *u.CPUEfficiency != 0 {
		t.Errorf("a 20 s job at 0 CPU is a measured 0: %+v", u)
	}
	// real use is reported as such, and may exceed 1
	u = ParseUsage([][]string{row("79|COMPLETED|0:0|01:00:00|s|e|02:00:00|8G||4|03:00:00||1")})
	if u.CPUNotAccounted || u.CPUEfficiency == nil || math.Abs(*u.CPUEfficiency-0.75) > 1e-9 {
		t.Errorf("%+v", u)
	}
	// the job row has nothing but its steps do: the steps' sum is used
	u = ParseUsage([][]string{row("80|COMPLETED|0:0|01:00:00|s|e|02:00:00|8G||4|00:00:00||1"), row("80.batch|COMPLETED|0:0|01:00:00|s|e||||4|01:00:00|1G|1"), row("80.0|COMPLETED|0:0|01:00:00|s|e||||4|01:00:00|1G|1")})
	if u.CPUNotAccounted || u.CPUEfficiency == nil || math.Abs(*u.CPUEfficiency-0.5) > 1e-9 {
		t.Errorf("%+v", u)
	}
}

func TestUsageOOMAndTimeout(t *testing.T) {
	// the batch step was killed for memory although the job row says FAILED
	u := ParseUsage([][]string{
		row("5|FAILED|0:125|00:41:00|s|e|1-00:00:00|64G|billing=16,cpu=16,mem=64G,node=1|16|05:00:00||1"),
		row("5.batch|OUT_OF_MEMORY|0:125|00:41:00|s|e||||16|05:00:00|65011712K|1"),
	})
	if !u.OOMKilled || u.State != "FAILED" {
		t.Errorf("%+v", u)
	}
	// the job row itself says OUT_OF_MEMORY
	if u := ParseUsage([][]string{row("6|OUT_OF_MEMORY|0:125|00:41:00|s|e|1-00:00:00|64G||16|05:00:00||1")}); !u.OOMKilled {
		t.Error("job row OOM not seen")
	}
	u = ParseUsage([][]string{row("7|TIMEOUT|0:0|1-00:00:00|s|e|1-00:00:00|64G||16|10:00:00||1")})
	if u.OOMKilled || u.State != "TIMEOUT" || u.TimelimitSec == nil || u.ElapsedSeconds != *u.TimelimitSec {
		t.Errorf("%+v", u)
	}
	if u := ParseUsage([][]string{row("8|CANCELLED by 1000|0:0|00:01:00|s|e|01:00:00|1G||1|00:00:30||1")}); u.State != "CANCELLED" {
		t.Errorf("state word %q", u.State)
	}
}

func TestRequestedMemoryMultipliesPerCPUAndPerNode(t *testing.T) {
	two, one := int64(2), int64(1)
	for _, c := range []struct {
		req, tres string
		cpus      *int64
		nodes     *int64
		want      int64 // 0: unknown
	}{
		{"4000Mc", "", &two, &one, 8000 << 20},
		{"64Gn", "", &two, &two, 128 << 30},
		{"64G", "", &two, &two, 64 << 30},
		{"4000Mc", "billing=2,cpu=2,mem=8000M,node=1", &two, &one, 8000 << 20}, // TRES is already the total
		{"", "", &two, &one, 0},
		{"4000Mc", "", nil, nil, 0}, // per CPU with no CPU count: not guessed
		{"0", "", &two, &one, 0},
	} {
		got := reqMem(c.req, c.tres, c.cpus, c.nodes)
		switch {
		case c.want == 0 && got != nil:
			t.Errorf("%q %q: %d, want unknown", c.req, c.tres, *got)
		case c.want != 0 && (got == nil || *got != c.want):
			t.Errorf("%q %q: %v, want %d", c.req, c.tres, got, c.want)
		}
	}
}

func TestBatchStatusCarriesUsageAndStepsDoNotDisturbTheState(t *testing.T) {
	out := "12|RUNNING\n---\n" +
		"12|RUNNING|0:0|00:00:01|s|Unknown|02:00:00|8G||2|00:00:01||1\n" +
		"14|TIMEOUT|0:15|01:00:00|s|e|01:00:00|8G|cpu=2,mem=8G|2|00:30:00||1\n" +
		"14.batch|CANCELLED|0:15|01:00:00|s|e||||2|00:30:00|2097152K|1\n" +
		"14.0|CANCELLED|0:15|01:00:00|s|e||||2|00:00:01|1024K|1\n"
	got := ParseBatchStatus(out, []string{"12", "14", "99"})
	if got["14"].State != StateFailed || got["14"].Slurm != "TIMEOUT" || got["14"].ExitCode != "0:15" {
		t.Errorf("state of a job with steps: %+v", got["14"])
	}
	if u := got["14"].Usage; u == nil || u.MaxRSSBytes == nil || *u.MaxRSSBytes != 2<<30 || u.State != "TIMEOUT" {
		t.Errorf("usage: %+v", u)
	}
	if got["12"].State != StateRunning || got["99"].Usage != nil || got["99"].State != "" {
		t.Errorf("%+v %+v", got["12"], got["99"])
	}
	for _, id := range []string{"1", "12"} {
		cmd, err := StatusCommand(id)
		if err != nil || !strings.Contains(cmd, UsageFields) || strings.Contains(cmd, " -X ") {
			t.Errorf("the single-job command must carry the same fields and the steps: %q %v", cmd, err)
		}
	}
}

func TestSuggestionMathAndWhenItIsWithheld(t *testing.T) {
	gib := int64(1) << 30
	rss, req := 79*gib, 140*gib
	u := &Usage{State: "COMPLETED", ElapsedSeconds: 6*3600 + 12*60 + 3, MaxRSSBytes: &rss, ReqMemBytes: &req}
	sg := u.SuggestRequest(0)
	// 79 x 1.3 = 102.7 GiB rounds up to 103G; 6:12:03 x 1.5 = 9:18:04.5 rounds up to 9:19
	if sg == nil || sg.Mem != "103G" || sg.Time != "09:19:00" || sg.MemBytes != int64(math.Ceil(float64(rss)*1.3)) || sg.TimeSeconds != 9*3600+19*60 {
		t.Fatalf("%+v", sg)
	}
	if sg := u.SuggestRequest(8 * 3600); sg.Time != "08:00:00" || !strings.Contains(sg.Basis, "capped") {
		t.Errorf("the partition limit caps the time: %+v", sg)
	}
	// more than a day uses Slurm's D-HH:MM:SS
	long := &Usage{State: "COMPLETED", ElapsedSeconds: 2 * 86400}
	if sg := long.SuggestRequest(0); sg == nil || sg.Time != "3-00:00:00" || sg.Mem != "" {
		t.Errorf("%+v", sg)
	}
	small := int64(300) << 20
	if sg := (&Usage{State: "COMPLETED", ElapsedSeconds: 60, MaxRSSBytes: &small}).SuggestRequest(0); sg.Mem != "512M" {
		t.Errorf("under a GiB: 300 MiB x 1.3 = 390 MiB in steps of 256 MiB: %+v", sg)
	}
	// a job killed for memory only shows what it had when it died: the suggestion is not below 1.5 x the request
	r64, p64 := 64*gib, 64*gib-1
	oom := &Usage{State: "FAILED", OOMKilled: true, ElapsedSeconds: 2460, MaxRSSBytes: &p64, ReqMemBytes: &r64}
	if sg := oom.SuggestRequest(0); sg == nil || sg.Mem != "96G" || sg.Time != "" || !strings.Contains(sg.Basis, "killed") {
		t.Errorf("OOM: %+v (a job stopped early says nothing about how long the work takes)", sg)
	}
	// a timeout: the time is 1.5 x the limit it reached, and says it is a floor
	tl := &Usage{State: "TIMEOUT", ElapsedSeconds: 86400}
	if sg := tl.SuggestRequest(0); sg == nil || sg.Time != "1-12:00:00" || !strings.Contains(sg.Basis, "longer") {
		t.Errorf("timeout: %+v", sg)
	}
	// nothing to base a number on, or a job whose run says nothing about the next one
	for _, u := range []*Usage{{State: "CANCELLED", ElapsedSeconds: 100, MaxRSSBytes: &rss}, {State: "FAILED", ElapsedSeconds: 100, MaxRSSBytes: &rss}, {State: "COMPLETED"}} {
		if sg := u.SuggestRequest(0); sg != nil {
			t.Errorf("%s: %+v", u.State, sg)
		}
	}
}

func TestSacctCommandWithAChosenColumnListAndItsClosingLine(t *testing.T) {
	min, err := BatchStatusCommandFields([]string{"12"}, Fields(MinimalFields))
	if err != nil || !strings.Contains(min, "sacct -n -P -X -j 12 -o "+MinimalFields+" ") {
		t.Fatalf("the minimal call is the one used before usage existed (jobs only): %q %v", min, err)
	}
	full, _ := BatchStatusCommandFields([]string{"12"}, Fields(UsageFields))
	if strings.Contains(full, "-X") {
		t.Errorf("steps are needed for MaxRSS: %q", full)
	}
	if _, err := BatchStatusCommandFields([]string{"12"}, []string{"JobID", "State; rm -rf ~"}); err == nil {
		t.Error("a column name outside the known list reached a command")
	}
	body, exit, msg := SplitSacctExit("12|RUNNING\n---\n" + sacctExitMark + "1\nsacct: error: Invalid field requested: \"ReqTRES\"\n")
	if exit != 1 || strings.Contains(body, "accounting-exit") || RejectedField(msg) != "ReqTRES" {
		t.Errorf("%q %d %q", body, exit, msg)
	}
	if _, exit, _ := SplitSacctExit("12|RUNNING\n---\n"); exit != 0 {
		t.Error("an answer without the closing line must read as success")
	}
	if RejectedField("sacct: error: Invalid field requested: \"Bogus\"") != "" || RejectedField("slurmdbd down") != "" {
		t.Error("only a column this program asks for can be named")
	}
	if !RowsFit("\n---\n12|COMPLETED|0:0|1|s|e\n", 6) || RowsFit("\n---\n12|COMPLETED|0:0|1|s|e\n", 13) || !RowsFit("12|RUNNING\n---\n", 13) {
		t.Error("RowsFit")
	}
	// a minimal answer parsed with the minimal columns gives the state and says nothing about memory
	st := ParseBatchStatusFields("\n---\n12|COMPLETED|0:0|00:00:09|s|e\n", []string{"12"}, Fields(MinimalFields))["12"]
	if st.State != StateCompleted || st.Usage == nil || st.Usage.ElapsedSeconds != 9 || st.Usage.MaxRSSBytes != nil || st.Usage.ReqMemBytes != nil || st.Usage.TimelimitSec != nil {
		t.Errorf("%+v", st)
	}
}

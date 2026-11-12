package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

func (e *watchEnv) sacctRows(slurmID string, rows ...string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.bin, "state", slurmID+".sacct"), []byte(strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *watchEnv) sshLines(sub string) int {
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(e.bin), "ssh.log"))
	return strings.Count(string(b), sub)
}

func TestEndedJobKeepsWhatSlurmReportedItUsed(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	ctx := context.Background()
	w := newJobWatcher(MinWatchInterval)
	a := e.submit(e.draft("vgt-a", nil), nil)
	e.setSlurm(a.SlurmID, "RUNNING")
	t0 := time.Now()
	e.srv.watchOnce(ctx, w, t0)
	if e.job(a.ID).Usage != nil {
		t.Fatal("a running job has no usage yet")
	}
	e.setSlurm(a.SlurmID, "COMPLETED")
	e.srv.watchOnce(ctx, w, t0.Add(2*time.Minute))
	e.srv.waitRecords()
	e.srv.waitImports()
	u := e.job(a.ID).Usage
	// the stand-in's accounting row: 12 s of CPU in 9 s on 2 CPUs, a 1 GiB peak on the .batch step, 4 GiB and 30 min requested
	if u == nil || u.State != "COMPLETED" || u.MaxRSSBytes == nil || *u.MaxRSSBytes != 1<<30 || u.ReqMemBytes == nil || *u.ReqMemBytes != 4<<30 ||
		u.TimelimitSec == nil || *u.TimelimitSec != 1800 || u.ElapsedSeconds != 9 || u.CPUEfficiency == nil || *u.CPUEfficiency < 0.66 || *u.CPUEfficiency > 0.67 || u.CPUNotAccounted {
		t.Fatalf("%+v", u)
	}
	// the job JSON carries it
	_, b := get(t, e.api("/api/jobs/"+itoa(int(a.ID))))
	var got struct {
		Usage *struct {
			MaxRSS  *int64 `json:"maxRssBytes"`
			Suggest *struct{ Mem, Time string }
		} `json:"usage"`
	}
	if json.Unmarshal(b, &got) != nil || got.Usage == nil || got.Usage.MaxRSS == nil || got.Usage.Suggest == nil || got.Usage.Suggest.Mem != "2G" || got.Usage.Suggest.Time != "00:01:00" {
		t.Errorf("%s", b)
	}

	// the Refresh button reaches the same record, the OOM kill on a step is kept, and TotalCPU 0 is not shown as 0 %
	c := e.submit(e.draft("rohsa-like", nil), nil)
	e.setSlurm(c.SlurmID, "FAILED")
	e.sacctRows(c.SlurmID,
		c.SlurmID+"|FAILED|0:125|06:12:03|s|e|3-00:00:00|140G|billing=32,cpu=32,mem=140G,node=1|32|00:00:00||1",
		c.SlurmID+".batch|OUT_OF_MEMORY|0:125|06:12:03|s|e||||32|00:00:00|146800640K|1")
	if r, b := postJSON(t, e.api("/api/jobs/"+itoa(int(c.ID))+"/refresh"), map[string]any{}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	u = e.job(c.ID).Usage
	if u == nil || !u.OOMKilled || !u.CPUNotAccounted || u.CPUEfficiency != nil || u.MaxRSSBytes == nil || *u.MaxRSSBytes != 140<<30 || u.Suggest == nil || u.Suggest.Mem != "210G" {
		t.Fatalf("%+v", u)
	}
	// a second look at an ended job changes nothing (the end is read once)
	e.sacctRows(c.SlurmID, c.SlurmID+"|FAILED|0:0|00:00:01|s|e|00:01:00|1G||1|00:00:00||1")
	postJSON(t, e.api("/api/jobs/"+itoa(int(c.ID))+"/refresh"), map[string]any{})
	if got := e.job(c.ID).Usage; got == nil || got.ElapsedSeconds != 22323 {
		t.Errorf("a refreshed end overwrote the first reading: %+v", got)
	}
}

func TestRerunOffersTheLastJobsMeasuredUse(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	ctx := context.Background()
	w := newJobWatcher(MinWatchInterval)
	a := e.submit(e.draft("vgt-a", nil), nil)
	e.setSlurm(a.SlurmID, "COMPLETED")
	e.srv.watchOnce(ctx, w, time.Now())
	e.srv.waitRecords()
	e.srv.waitImports()
	run, err := e.runs.Save(workspace.Local, "local-owner", "/x/run-a", runs.Parsed{Run: runs.Run{Name: "run-a", Kind: "vgt"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.jobs.LinkRun(a.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	_, b := get(t, e.api("/api/runs/"+itoa(int(run.ID))+"/rerun"))
	var out struct {
		LastJob *struct {
			ID    int64
			Name  string
			Usage *jobs.Usage
		} `json:"lastJob"`
	}
	if json.Unmarshal(b, &out) != nil || out.LastJob == nil || out.LastJob.ID != a.ID || out.LastJob.Usage == nil || out.LastJob.Usage.MaxRSSBytes == nil {
		t.Errorf("%s", b)
	}
	// an imported run has no job and so no measurement to offer
	imp, _ := e.runs.Save(workspace.Local, "local-owner", "/x/run-b", runs.Parsed{Run: runs.Run{Name: "run-b", Kind: "vgt"}}, nil)
	_, b = get(t, e.api("/api/runs/"+itoa(int(imp.ID))+"/rerun"))
	if !strings.Contains(string(b), `"lastJob":null`) {
		t.Errorf("%s", b)
	}
}

type storageBody struct {
	Report *struct {
		Quota *struct {
			Tool    string
			Entries []struct{ UsedBytes int64 }
		}
		QuotaNote   string
		Filesystems []struct {
			Root       string
			TotalBytes int64
			Error      string
		}
	} `json:"report"`
	Error     string     `json:"error"`
	CheckedAt *time.Time `json:"checkedAt"`
	Cached    bool       `json:"cached"`
}

func (e *watchEnv) storage(method, suffix string) storageBody {
	e.t.Helper()
	url := e.api("/api/hosts/" + itoa(int(e.host.ID)) + "/storage" + suffix)
	var b []byte
	var code int
	if method == "GET" {
		r, body := get(e.t, url)
		code, b = r.StatusCode, body
	} else {
		r, body := postJSON(e.t, url, map[string]any{})
		code, b = r.StatusCode, body
	}
	if code != 200 {
		e.t.Fatalf("%s %s: %d %s", method, url, code, b)
	}
	var out storageBody
	if err := json.Unmarshal(b, &out); err != nil {
		e.t.Fatalf("%v: %s", err, b)
	}
	return out
}

func TestHostStorageIsCachedAndRefreshIsRateLimited(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	os.WriteFile(filepath.Join(e.bin, "beegfs-ctl"), []byte("#!/bin/sh\ncat <<'EOF'\n"+`Quota information for storage pool Default (ID: 1):

      user/group     ||           size          ||    chunk files    
     name     |  id  ||    used    |    hard    ||  used   |  hard   
--------------|------||------------|------------||---------|---------
      spectre|  1000||  395.20 GiB|  400.00 GiB||  1234567|unlimited
`+"EOF\n"), 0o755)

	first := e.storage("GET", "")
	if first.Cached || first.Report == nil || first.Report.Quota == nil || first.Report.Quota.Tool != "beegfs-ctl" || len(first.Report.Filesystems) != 1 ||
		first.Report.Filesystems[0].Root != e.remoteRoot || first.Report.Filesystems[0].TotalBytes <= 0 || first.CheckedAt == nil {
		t.Fatalf("%+v", first)
	}
	if n := e.sshLines("beegfs-ctl --getquota"); n != 1 {
		t.Fatalf("%d looks", n)
	}
	// the screen asking again, and the Refresh button pressed at once, cost no second call
	if again := e.storage("GET", ""); !again.Cached || again.Report == nil || e.sshLines("beegfs-ctl --getquota") != 1 {
		t.Errorf("a fresh look was not reused: %+v", again)
	}
	if again := e.storage("POST", "/refresh"); !again.Cached || e.sshLines("beegfs-ctl --getquota") != 1 {
		t.Errorf("Refresh inside the minimum spacing reached the host again: %+v", again)
	}
	// later, Refresh looks again; an old look is replaced on the next ask even without Refresh
	e.srv.disk.mu.Lock()
	e.srv.disk.look[e.host.ID].at = time.Now().Add(-time.Minute)
	e.srv.disk.mu.Unlock()
	if r := e.storage("POST", "/refresh"); r.Cached || e.sshLines("beegfs-ctl --getquota") != 2 {
		t.Errorf("Refresh after the spacing did not look: %+v", r)
	}
	e.srv.disk.mu.Lock()
	e.srv.disk.look[e.host.ID].at = time.Now().Add(-storageFresh - time.Second)
	e.srv.disk.mu.Unlock()
	if r := e.storage("GET", ""); r.Cached || e.sshLines("beegfs-ctl --getquota") != 3 {
		t.Errorf("a look older than ten minutes was reused: %+v", r)
	}

	// the host goes away: the error is said, the last good look is kept and labelled with its time, and the failure is
	// not retried at once either
	os.WriteFile(filepath.Join(e.home, "DOWN"), nil, 0o644)
	e.srv.disk.mu.Lock()
	e.srv.disk.look[e.host.ID].at = time.Now().Add(-storageFresh - time.Second)
	e.srv.disk.mu.Unlock()
	down := e.storage("GET", "")
	if down.Error == "" || down.Report == nil || down.CheckedAt == nil {
		t.Fatalf("%+v", down)
	}
	calls := e.sshLines("df -P")
	if again := e.storage("GET", ""); again.Error == "" || !again.Cached || e.sshLines("df -P") != calls {
		t.Errorf("a failed look was repeated at once: %+v", again)
	}
}

func TestHostStorageWithoutAQuotaToolSaysSoAndKeepsFreeSpace(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	// a stand-in `quota` that prints nothing readable, and no other tool: this machine's own quota (if any) is not on the
	// remote PATH of the stand-in only when it is shadowed, so shadow it
	os.WriteFile(filepath.Join(e.bin, "quota"), []byte("#!/bin/sh\necho 'quota: Cannot open quotafile /home/aquota.user: Permission denied' >&2\n"), 0o755)
	r := e.storage("GET", "")
	if r.Report == nil || r.Report.Quota != nil || !strings.Contains(r.Report.QuotaNote, "quota tool is not available") || len(r.Report.Filesystems) != 1 || r.Report.Filesystems[0].TotalBytes <= 0 {
		t.Fatalf("%+v", r.Report)
	}
}

func TestRunFolderSizesAreMeasuredOnDemandBoundedToTheRootsAndSortedBySize(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	small := e.submit(e.draft("small-run", nil), nil)
	big := e.submit(e.draft("big-run", nil), nil)
	for dir, kib := range map[string]int{small.OutputDir: 64, big.OutputDir: 2048} {
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "out.bin"), make([]byte, kib<<10), 0o644)
	}
	// a run folder found by scanning a project on this host
	scanned := filepath.Join(e.remoteRoot, "proj", "run-found")
	os.MkdirAll(scanned, 0o755)
	os.WriteFile(filepath.Join(scanned, "m.bin"), make([]byte, 512<<10), 0o644)
	ps, err := projects.Open(e.srv.Store.DB())
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Projects = ps
	p, err := ps.Touch(workspace.Local, "local-owner", e.host.ID, filepath.Join(e.remoteRoot, "proj"), "proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := ps.SaveScan(workspace.Local, p.ID, time.Now(), json.RawMessage(`{}`), []projects.Row{{Group: "runs", Parent: "", Name: "run-found", Kind: "run", Path: "run-found", Data: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}

	base := "/api/hosts/" + itoa(int(e.host.ID))
	type folder struct {
		Path       string
		Name, Kind string
		JobID      *int64
		Bytes      *int64
		Partial    bool
	}
	list := func() []folder {
		r, b := get(t, e.api(base+"/folders"))
		if r.StatusCode != 200 {
			t.Fatalf("%d %s", r.StatusCode, b)
		}
		var out struct{ Folders []folder }
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		return out.Folders
	}
	before := e.sshLines("du -sk")
	fs := list()
	if len(fs) != 3 || e.sshLines("du -sk") != before {
		t.Fatalf("listing must not touch the host: %+v", fs)
	}
	for _, f := range fs {
		if f.Bytes != nil {
			t.Errorf("unmeasured is not zero: %+v", f)
		}
		if f.Name == "run-found" && (f.Kind != "run" || f.JobID != nil) || f.Name == "big-run" && (f.Kind != "job" || f.JobID == nil) {
			t.Errorf("%+v", f)
		}
	}
	measure := func(p string) (int, []byte) {
		r, b := postJSON(t, e.api(base+"/folders/size"), map[string]any{"path": p})
		return r.StatusCode, b
	}
	for _, p := range []string{small.OutputDir, big.OutputDir, scanned} {
		if code, b := measure(p); code != 200 {
			t.Fatalf("%s: %d %s", p, code, b)
		}
	}
	fs = list()
	if len(fs) != 3 || fs[0].Name != "big-run" || fs[1].Name != "run-found" || fs[2].Name != "small-run" || fs[0].Bytes == nil || *fs[0].Bytes < 2<<20 || *fs[2].Bytes < 64<<10 || *fs[2].Bytes >= *fs[0].Bytes {
		t.Fatalf("largest first: %+v", fs)
	}
	if e.sshLines("du -sk") != before+3 {
		t.Errorf("one du per measurement, none for the listing")
	}
	// only the host's roots; no shell reaches du
	for _, p := range []string{"/etc", filepath.Join(e.remoteRoot, ".."), "relative/path", ""} {
		if code, b := measure(p); code != 422 {
			t.Errorf("%q: %d %s, want 422", p, code, b)
		}
	}
	// lexically inside the root, so it is asked about, but as one quoted argument: du finds no such folder
	if code, _ := measure(e.remoteRoot + "/x; touch /tmp/pwned"); code != 502 {
		t.Errorf("a name with shell syntax in it: %d", code)
	}
	if code, _ := measure(filepath.Join(e.remoteRoot, "missing")); code != 502 {
		t.Errorf("a folder that is not there must fail, not show 0 bytes")
	}
	// the owner's loopback listener only
	req, _ := http.NewRequest("POST", e.api(base+"/folders/size"), strings.NewReader(`{"path":"`+scanned+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("a cross-site request: %d, want 403", resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", e.api(base+"/storage"), nil)
	req.Host = "attacker.example"
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("a non-loopback Host: %d, want 403", resp.StatusCode)
	}
	if _, err := os.Stat("/tmp/pwned"); err == nil {
		t.Error("a path reached a shell")
	}
	// nothing was deleted
	for _, d := range []string{small.OutputDir, big.OutputDir, scanned} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s is gone", d)
		}
	}
}

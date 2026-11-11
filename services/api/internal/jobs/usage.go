package jobs

import (
	"math"
	"strconv"
	"strings"
)

// What a finished job used (ADR 0044). The numbers are what Slurm's accounting (sacct) reported, kept as reported: a
// value sacct did not give is nil, never 0, and nothing here is a verdict on whether the job's result is valid.

// Usage is what sacct said about one finished job. Pointers are "not reported".
type Usage struct {
	State          string   `json:"state"`    // the Slurm state word of the job (COMPLETED, TIMEOUT, OUT_OF_MEMORY, ...)
	ExitCode       string   `json:"exitCode"` // "code:signal" as sacct prints it
	ElapsedSeconds int64    `json:"elapsedSeconds"`
	TimelimitSec   *int64   `json:"timelimitSeconds"` // nil for UNLIMITED, Partition_Limit or no value
	ReqMemBytes    *int64   `json:"reqMemBytes"`      // total requested memory
	MaxRSSBytes    *int64   `json:"maxRssBytes"`      // largest peak resident set of any single step; nil when accounting recorded none
	AllocCPUS      *int64   `json:"allocCpus"`
	TotalCPUSec    *float64 `json:"totalCpuSeconds"`
	// CPUEfficiency is TotalCPU / (Elapsed x AllocCPUS), nil when it cannot be computed or when CPUNotAccounted.
	CPUEfficiency *float64 `json:"cpuEfficiency"`
	// CPUNotAccounted: TotalCPU is about zero over a long run, which means the work ran in a process the scheduler does
	// not account (not that the job was idle). The efficiency is then not shown.
	CPUNotAccounted bool `json:"cpuNotAccounted"`
	// OOMKilled: the job or one of its steps was ended by Slurm for exceeding its memory.
	OOMKilled bool        `json:"oomKilled"`
	Suggest   *Suggestion `json:"suggest"`
}

// Suggestion is a request sized from one finished job's measured use. It is only ever shown; the researcher applies it.
type Suggestion struct {
	Mem         string `json:"mem"`  // a Slurm memory request such as "93G"; empty when memory use was not measured
	Time        string `json:"time"` // a Slurm time limit such as "09:18:00"; empty when the elapsed time is not representative
	MemBytes    int64  `json:"memBytes"`
	TimeSeconds int64  `json:"timeSeconds"`
	// Basis says, in words, what the numbers were derived from, so the person can disagree with it.
	Basis string `json:"basis"`
}

const (
	// minAccountedSeconds: below this a run is too short to call a near-zero TotalCPU "not accounted".
	minAccountedSeconds = 60
	// notAccountedBelow: TotalCPU under this share of Elapsed x AllocCPUS counts as "about zero".
	notAccountedBelow = 0.005
	maxSizeBytes      = int64(1) << 60
)

// UsageFields is the sacct column list the status commands ask for. The first six are what the status parser has always
// read; the rest feed Usage. Steps (JobID "12.batch", "12.0") are included because MaxRSS is recorded on steps only.
const UsageFields = "JobID,State,ExitCode,Elapsed,Start,End,Timelimit,ReqMem,ReqTRES,AllocCPUS,TotalCPU,MaxRSS,NNodes"

// parseDuration reads Slurm's [D-]HH:MM:SS[.mmm] and MM:SS[.mmm]. Anything else (UNLIMITED, a bare number, garbage) is not ok.
func parseDuration(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 32 {
		return 0, false
	}
	var days float64
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.ParseUint(d, 10, 32)
		if err != nil {
			return 0, false
		}
		days, s = float64(n), rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var t float64
	for i, p := range parts {
		last := i == len(parts)-1
		if !last && strings.Contains(p, ".") {
			return 0, false
		}
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		t = t*60 + v
	}
	return days*86400 + t, true
}

// parseSize reads a Slurm size such as "81234K", "140G", "1.5T", "4000Mc" or "140Gn" into bytes (units are powers of 1024,
// as Slurm's are). bare is the unit of a number with no suffix. perCPU and perNode report a trailing c or n.
func parseSize(s string, bare int64) (bytes int64, perCPU, perNode, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 32 {
		return 0, false, false, false
	}
	switch s[len(s)-1] {
	case 'c':
		perCPU, s = true, s[:len(s)-1]
	case 'n':
		perNode, s = true, s[:len(s)-1]
	}
	unit := bare
	if s != "" {
		switch s[len(s)-1] {
		case 'K', 'k':
			unit, s = 1<<10, s[:len(s)-1]
		case 'M', 'm':
			unit, s = 1<<20, s[:len(s)-1]
		case 'G', 'g':
			unit, s = 1<<30, s[:len(s)-1]
		case 'T', 't':
			unit, s = 1<<40, s[:len(s)-1]
		case 'P', 'p':
			unit, s = 1<<50, s[:len(s)-1]
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) || v*float64(unit) > float64(maxSizeBytes) {
		return 0, false, false, false
	}
	return int64(math.Round(v * float64(unit))), perCPU, perNode, true
}

func atoi(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n, err == nil && n >= 0 && n < 1<<31
}

// reqMem is the total memory requested: ReqTRES "mem=140G" when present (it is already the total), else ReqMem, whose
// trailing c (per CPU) and n (per node) are multiplied out.
func reqMem(reqMem, reqTRES string, cpus, nodes *int64) *int64 {
	for _, kv := range strings.Split(reqTRES, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "mem" {
			if b, _, _, ok := parseSize(v, 1<<20); ok && b > 0 {
				return &b
			}
		}
	}
	b, perCPU, perNode, ok := parseSize(reqMem, 1<<20)
	if !ok || b == 0 {
		return nil
	}
	switch {
	case perCPU:
		if cpus == nil || *cpus == 0 || b > maxSizeBytes/(*cpus) {
			return nil
		}
		b *= *cpus
	case perNode:
		n := int64(1)
		if nodes != nil && *nodes > 0 {
			n = *nodes
		}
		b *= n
	}
	return &b
}

// ParseUsage builds a Usage from the sacct rows of one job (see UsageFields), each already split on "|": the job's own row
// and its steps. It returns nil when sacct gave no row for the job itself.
func ParseUsage(rows [][]string) *Usage {
	var job []string
	var peak int64
	var peakSeen, oom bool
	var stepCPU float64
	var stepCPUSeen bool
	col := func(r []string, i int) string {
		if i < len(r) {
			return strings.TrimSpace(r[i])
		}
		return ""
	}
	for _, r := range rows {
		id := col(r, 0)
		if strings.HasPrefix(strings.ToUpper(col(r, 1)), "OUT_OF_MEMORY") {
			oom = true
		}
		if !strings.Contains(id, ".") {
			if job == nil {
				job = r
			}
			continue
		}
		if b, _, _, ok := parseSize(col(r, 11), 1<<10); ok && b > 0 { // MaxRSS: a bare number is KiB
			peakSeen = true
			if b > peak {
				peak = b
			}
		}
		if t, ok := parseDuration(col(r, 10)); ok {
			stepCPU, stepCPUSeen = stepCPU+t, true
		}
	}
	if job == nil {
		return nil
	}
	u := &Usage{ExitCode: col(job, 2), OOMKilled: oom}
	if w := strings.Fields(col(job, 1)); len(w) > 0 { // "CANCELLED by 1000" is CANCELLED
		u.State = strings.ToUpper(w[0])
	}
	if e, ok := parseDuration(col(job, 3)); ok {
		u.ElapsedSeconds = int64(math.Round(e))
	}
	if t, ok := parseDuration(col(job, 6)); ok {
		n := int64(math.Round(t))
		u.TimelimitSec = &n
	}
	if n, ok := atoi(col(job, 9)); ok {
		u.AllocCPUS = &n
	}
	var nodes *int64
	if n, ok := atoi(col(job, 12)); ok {
		nodes = &n
	}
	u.ReqMemBytes = reqMem(col(job, 7), col(job, 8), u.AllocCPUS, nodes)
	if peakSeen {
		u.MaxRSSBytes = &peak
	}
	if t, ok := parseDuration(col(job, 10)); ok {
		u.TotalCPUSec = &t
		if t == 0 && stepCPUSeen && stepCPU > 0 { // the job row summed nothing but its steps did
			u.TotalCPUSec = &stepCPU
		}
	} else if stepCPUSeen {
		u.TotalCPUSec = &stepCPU
	}
	if u.TotalCPUSec != nil && u.AllocCPUS != nil && *u.AllocCPUS > 0 && u.ElapsedSeconds > 0 {
		capacity := float64(u.ElapsedSeconds) * float64(*u.AllocCPUS)
		eff := *u.TotalCPUSec / capacity
		if u.ElapsedSeconds >= minAccountedSeconds && eff < notAccountedBelow {
			u.CPUNotAccounted = true
		} else {
			u.CPUEfficiency = &eff
		}
	}
	u.Suggest = u.SuggestRequest(0)
	return u
}

const (
	memHeadroom  = 1.3
	timeHeadroom = 1.5
)

// SuggestRequest sizes the next request from this job: memory = peak x 1.3 rounded up, time = elapsed x 1.5 rounded up
// to a minute and capped at partitionMaxSec when that is known (0 = not known). A job that ran out of memory only shows
// the memory it had when it was killed, so its suggestion is the larger of peak x 1.3 and the request x 1.5. Elapsed time
// is suggested only for a job that ran to its end or to its limit: a job stopped early says nothing about how long the
// work takes. nil when there is nothing to base a number on.
func (u *Usage) SuggestRequest(partitionMaxSec int64) *Suggestion {
	ran := u.State == "COMPLETED" || u.State == "TIMEOUT"
	if !ran && !u.OOMKilled {
		return nil
	}
	sg := &Suggestion{}
	var basis []string
	if u.MaxRSSBytes != nil || (u.OOMKilled && u.ReqMemBytes != nil) {
		need := 0.0
		if u.MaxRSSBytes != nil {
			need = float64(*u.MaxRSSBytes) * memHeadroom
		}
		if u.OOMKilled {
			if u.ReqMemBytes != nil {
				need = math.Max(need, float64(*u.ReqMemBytes)*timeHeadroom)
			}
			basis = append(basis, "it was killed for exceeding its memory, so its true need is higher than the peak it reached: the larger of 1.3 x peak and 1.5 x the request")
		} else {
			basis = append(basis, "1.3 x the measured peak memory")
		}
		sg.MemBytes = int64(math.Ceil(need))
		sg.Mem = slurmMem(sg.MemBytes)
	}
	if ran && u.ElapsedSeconds > 0 {
		t := int64(math.Ceil(float64(u.ElapsedSeconds)*timeHeadroom/60)) * 60
		note := "1.5 x the elapsed time"
		if u.State == "TIMEOUT" {
			note = "1.5 x the time limit it reached (it stopped at the limit, so the real run is longer)"
		}
		if partitionMaxSec > 0 && t > partitionMaxSec {
			t, note = partitionMaxSec, note+", capped at the partition's limit"
		}
		sg.TimeSeconds, sg.Time = t, slurmTime(t)
		basis = append(basis, note)
	}
	if sg.Mem == "" && sg.Time == "" {
		return nil
	}
	sg.Basis = strings.Join(basis, "; ")
	return sg
}

// slurmMem renders bytes as a --mem value, rounding up: whole GiB from 1 GiB, otherwise steps of 256 MiB.
func slurmMem(b int64) string {
	const gib, step = int64(1) << 30, int64(256) << 20
	if b >= gib {
		return strconv.FormatInt((b+gib-1)/gib, 10) + "G"
	}
	n := (b + step - 1) / step
	if n < 1 {
		n = 1
	}
	return strconv.FormatInt(n*256, 10) + "M"
}

// slurmTime renders seconds (a whole number of minutes) as D-HH:MM:SS, or HH:MM:SS under a day.
func slurmTime(t int64) string {
	d, h, m := t/86400, t%86400/3600, t%3600/60
	if d > 0 {
		return strconv.FormatInt(d, 10) + "-" + pad2(h) + ":" + pad2(m) + ":00"
	}
	return pad2(h) + ":" + pad2(m) + ":00"
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

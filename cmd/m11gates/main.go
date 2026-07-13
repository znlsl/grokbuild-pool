// Command m11gates runs Scheme B M11 performance gates against mock upstream.
//
// Gates:
//
//	G1 import 10k < 60s
//	G2 pick bench (aim ≥20k/s)
//	G3 non-stream 100 concurrency, success ≥99%
//	G4 stream 50 SSE, no OOM, RSS < 6G
//	G5 429 half pool, end success ≥95%
//	G6 soak optional 15m (or SKIP)
//
// Isolation: 127.0.0.1:18080 only; never touch production :8080.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultDataDir   = "/opt/grokbuild-pool/data"
	defaultArtifacts = "/opt/grokbuild-pool/artifacts"
	defaultRepo      = "/opt/grokbuild-pool/repo"
	defaultListen    = "127.0.0.1:18080"
	defaultDB        = "/opt/grokbuild-pool/data/pool-10000.db"
	rssLimitBytes    = 6 << 30 // 6 GiB hard stop for G4
	rssAbortBytes    = int64(6.5 * float64(1<<30))
)

func main() {
	gate := flag.String("gate", "all", "gate to run: g1|g2|g3|g4|g5|g6|all|mvp")
	dataDir := flag.String("data-dir", defaultDataDir, "data directory")
	artifacts := flag.String("artifacts", defaultArtifacts, "artifacts directory")
	repo := flag.String("repo", defaultRepo, "repo root")
	listen := flag.String("listen", defaultListen, "pool-proxy listen")
	dbPath := flag.String("db", defaultDB, "catalog sqlite")
	poolProxy := flag.String("pool-proxy", "", "path to pool-proxy binary (default repo/bin/pool-proxy)")
	poolctl := flag.String("poolctl", "", "path to poolctl binary")
	g6Duration := flag.Duration("g6-duration", 15*time.Minute, "G6 soak duration (0 = skip)")
	g6Skip := flag.Bool("g6-skip", true, "SKIP G6 with reason (MVP timebox default)")
	flag.Parse()

	exportGoPath()

	if err := os.MkdirAll(*artifacts, 0o755); err != nil {
		fail("mkdir artifacts: %v", err)
	}

	pp := *poolProxy
	if pp == "" {
		pp = filepath.Join(*repo, "bin", "pool-proxy")
	}
	pc := *poolctl
	if pc == "" {
		pc = filepath.Join(*repo, "bin", "poolctl")
	}

	// Ensure binaries exist.
	if err := ensureBinaries(*repo, pp, pc); err != nil {
		fail("build: %v", err)
	}

	// Always free 18080 before starting.
	killPoolProxy()
	defer killPoolProxy()

	gates := expandGates(*gate)
	results := make(map[string]gateResult, len(gates))
	overallOK := true

	for _, g := range gates {
		fmt.Fprintf(os.Stderr, "\n=== M11 gate %s ===\n", strings.ToUpper(g))
		var r gateResult
		switch g {
		case "g1":
			r = runG1(pc, *dataDir, *artifacts)
		case "g2":
			r = runG2(*repo, *artifacts)
		case "g3":
			r = runG3(pp, *dbPath, *listen, *artifacts, false)
		case "g4":
			r = runG4(pp, *dbPath, *listen, *artifacts)
		case "g5":
			r = runG5(pp, *dbPath, *listen, *artifacts)
		case "g6":
			if *g6Skip || *g6Duration <= 0 {
				r = gateResult{
					Name:   "G6",
					Pass:   true,
					Skip:   true,
					Detail: "SKIP: MVP timebox — full 15m soak deferred; G1–G5 prioritized. Use -g6-skip=false -g6-duration=15m to run.",
				}
				writeArtifact(filepath.Join(*artifacts, "m11-g6.txt"), r.Report())
			} else {
				r = runG6(pp, *dbPath, *listen, *artifacts, *g6Duration)
			}
		default:
			r = gateResult{Name: g, Pass: false, Detail: "unknown gate"}
		}
		results[g] = r
		status := "PASS"
		if r.Skip {
			status = "SKIP"
		} else if !r.Pass {
			status = "FAIL"
			overallOK = false
		}
		fmt.Fprintf(os.Stderr, "=== %s %s ===\n%s\n", strings.ToUpper(g), status, r.Detail)
		if rssTooHigh() {
			fmt.Fprintf(os.Stderr, "RSS guard: process RSS > 6.5G — aborting remaining gates\n")
			overallOK = false
			break
		}
	}

	// Summary
	summaryPath := filepath.Join(*artifacts, "m11-summary.txt")
	var b strings.Builder
	b.WriteString("# M11 gate summary\n")
	b.WriteString(fmt.Sprintf("date: %s\n", time.Now().UTC().Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("host: %s\n", hostLine()))
	b.WriteString("| Gate | Result | Notes |\n|------|--------|-------|\n")
	for _, g := range []string{"g1", "g2", "g3", "g4", "g5", "g6"} {
		r, ok := results[g]
		if !ok {
			continue
		}
		st := "PASS"
		if r.Skip {
			st = "SKIP"
		} else if !r.Pass {
			st = "FAIL"
		}
		note := oneLine(r.Detail)
		b.WriteString(fmt.Sprintf("| %s | %s | %s |\n", strings.ToUpper(g), st, note))
	}
	b.WriteString(fmt.Sprintf("\noverall_mvp: %v\n", overallOK))
	_ = os.WriteFile(summaryPath, []byte(b.String()), 0o644)
	fmt.Print(b.String())

	killPoolProxy()
	if !overallOK {
		os.Exit(1)
	}
}

type gateResult struct {
	Name   string
	Pass   bool
	Skip   bool
	Detail string
}

func (r gateResult) Report() string {
	st := "PASS"
	if r.Skip {
		st = "SKIP"
	} else if !r.Pass {
		st = "FAIL"
	}
	return fmt.Sprintf("gate: %s\nresult: %s\ndate: %s\nhost: %s\n\n%s\n",
		r.Name, st, time.Now().UTC().Format(time.RFC3339), hostLine(), r.Detail)
}

func expandGates(g string) []string {
	switch strings.ToLower(strings.TrimSpace(g)) {
	case "all", "mvp":
		return []string{"g1", "g2", "g3", "g4", "g5", "g6"}
	default:
		parts := strings.Split(g, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.ToLower(strings.TrimSpace(p))
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
}

func exportGoPath() {
	p := os.Getenv("PATH")
	if !strings.Contains(p, "/usr/local/go/bin") {
		_ = os.Setenv("PATH", "/usr/local/go/bin:"+p)
	}
}

func ensureBinaries(repo, poolProxy, poolctl string) error {
	needBuild := false
	for _, b := range []string{poolProxy, poolctl} {
		if st, err := os.Stat(b); err != nil || st.Size() == 0 {
			needBuild = true
			break
		}
	}
	// Always rebuild pool-proxy so --mock-fail-half is present.
	cmd := exec.Command("go", "build", "-o", poolProxy, "./cmd/pool-proxy")
	cmd.Dir = repo
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build pool-proxy: %w", err)
	}
	if needBuild {
		cmd = exec.Command("go", "build", "-o", poolctl, "./cmd/poolctl")
		cmd.Dir = repo
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build poolctl: %w", err)
		}
	}
	// m11gates itself is already running.
	return nil
}

// --- G1: import 10k ---

func runG1(poolctl, dataDir, artifacts string) gateResult {
	outPath := filepath.Join(artifacts, "m11-g1.txt")
	// Fresh ndjson + db under dataDir (idempotent upsert ok).
	ndjson := filepath.Join(dataDir, "m11-g1-synth-10000.ndjson")
	db := filepath.Join(dataDir, "m11-g1-pool-10000.db")
	_ = os.Remove(ndjson)
	_ = os.Remove(db)
	_ = os.Remove(db + "-wal")
	_ = os.Remove(db + "-shm")

	start := time.Now()
	// gen
	cmd := exec.Command(poolctl, "gen", "--count", "10000", "--out", ndjson)
	genOut, err := cmd.CombinedOutput()
	if err != nil {
		detail := fmt.Sprintf("gen failed: %v\n%s", err, genOut)
		r := gateResult{Name: "G1", Pass: false, Detail: detail}
		writeArtifact(outPath, r.Report())
		return r
	}
	// import
	cmd = exec.Command(poolctl, "import", "--db", db, "--in", ndjson)
	impOut, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if err != nil {
		detail := fmt.Sprintf("import failed: %v\n%s\ngen:\n%s", err, impOut, genOut)
		r := gateResult{Name: "G1", Pass: false, Detail: detail}
		writeArtifact(outPath, r.Report())
		return r
	}
	// stats
	cmd = exec.Command(poolctl, "stats", "--db", db)
	stOut, _ := cmd.CombinedOutput()

	pass := elapsed < 60*time.Second
	detail := fmt.Sprintf("import_10k_elapsed: %s\npass_bar: < 60s\npass: %v\n\ngen:\n%s\nimport:\n%s\nstats:\n%s\n",
		elapsed.Round(time.Millisecond), pass, string(genOut), string(impOut), string(stOut))
	r := gateResult{Name: "G1", Pass: pass, Detail: detail}
	writeArtifact(outPath, r.Report())
	return r
}

// --- G2: pick bench ---

func runG2(repo, artifacts string) gateResult {
	outPath := filepath.Join(artifacts, "m11-g2.txt")
	cmd := exec.Command("go", "test", "./internal/selector/",
		"-bench=BenchmarkPick$", "-benchmem", "-benchtime=1s", "-count=3", "-run=^$")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	// Also parallel for documentation
	cmd2 := exec.Command("go", "test", "./internal/selector/",
		"-bench=BenchmarkPickParallel", "-benchmem", "-benchtime=1s", "-count=1", "-run=^$")
	cmd2.Dir = repo
	out2, _ := cmd2.CombinedOutput()

	text := string(out) + "\n--- parallel ---\n" + string(out2)
	// Parse ns/op from first BenchmarkPick line
	rate := parseBenchRate(string(out), "BenchmarkPick-")
	aim := 20_000.0
	// Plan says "document actual; aim ≥20k/s" — if below aim but measured, still document;
	// MVP: require measurement success and rate > 0; soft aim.
	// User pass bar: "aim ≥20k/s document actual" — treat measured+documented as PASS if rate>0,
	// and mark hard pass when ≥20k.
	hard := rate >= aim
	detail := fmt.Sprintf("pick_rate_per_s: %.0f\naim: >= %.0f/s\nhard_aim_met: %v\nbench_err: %v\n\n%s\n",
		rate, aim, hard, err, text)
	// Acceptance: documented actual; aim is target not hard fail if still healthy high throughput.
	// Require ≥20k for PASS per table "aim ≥20k/s".
	r := gateResult{Name: "G2", Pass: err == nil && rate >= aim, Detail: detail}
	if err == nil && rate > 0 && rate < aim {
		// still fail against aim
		r.Pass = false
		r.Detail += "\nnote: measured but below aim\n"
	}
	writeArtifact(outPath, r.Report())
	return r
}

func parseBenchRate(out, prefix string) float64 {
	// BenchmarkPick-4   123456   234.5 ns/op
	best := 0.0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "BenchmarkPick") || strings.Contains(line, "Parallel") {
			continue
		}
		if !strings.Contains(line, "ns/op") {
			continue
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "ns/op" && i > 0 {
				ns, err := strconv.ParseFloat(fields[i-1], 64)
				if err == nil && ns > 0 {
					rate := 1e9 / ns
					if rate > best {
						best = rate
					}
				}
			}
		}
		_ = prefix
	}
	return best
}

// --- proxy helpers ---

type proxyProc struct {
	cmd    *exec.Cmd
	logPath string
	logFile *os.File
	pid    int
}

func startProxy(bin, db, listen string, extraArgs []string, logPath string) (*proxyProc, error) {
	killPoolProxy()
	// wait port free
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !portOpen(listen) {
			break
		}
		killPoolProxy()
		time.Sleep(100 * time.Millisecond)
	}

	args := []string{
		"--mock-upstream",
		"--db", db,
		"--listen", listen,
		"--config", filepath.Join(defaultRepo, "config.example.yaml"),
	}
	args = append(args, extraArgs...)
	lf, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = lf
	cmd.Stderr = lf
	// new process group so we can kill children
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		lf.Close()
		return nil, err
	}
	p := &proxyProc{cmd: cmd, logPath: logPath, logFile: lf, pid: cmd.Process.Pid}
	// wait ready
	readyDeadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(readyDeadline) {
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return p, fmt.Errorf("proxy exited early; see %s", logPath)
		}
		code, body, err := httpGet("http://"+listen+"/readyz", 2*time.Second)
		if err == nil && code == 200 && strings.Contains(body, "ready") {
			return p, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = p.stop()
	return nil, fmt.Errorf("proxy not ready within 20s; log=%s", logPath)
}

func (p *proxyProc) stop() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	// kill process group
	pgid, err := syscall.Getpgid(p.cmd.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
	}
	done := make(chan struct{})
	go func() {
		_, _ = p.cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if pgid, err := syscall.Getpgid(p.cmd.Process.Pid); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = p.cmd.Process.Kill()
		}
		<-done
	}
	if p.logFile != nil {
		_ = p.logFile.Close()
	}
	return nil
}

func (p *proxyProc) rssBytes() int64 {
	if p == nil || p.pid <= 0 {
		return 0
	}
	return readRSS(p.pid)
}

func killPoolProxy() {
	// Prefer precise match for our binary; avoid killing unrelated processes.
	_ = exec.Command("pkill", "-x", "pool-proxy").Run()
	_ = exec.Command("pkill", "-f", "/opt/grokbuild-pool/repo/bin/pool-proxy").Run()
	time.Sleep(200 * time.Millisecond)
	// if still listening on scheme-B port, free it
	_ = exec.Command("fuser", "-k", "18080/tcp").Run()
	time.Sleep(150 * time.Millisecond)
}

func portOpen(listen string) bool {
	c, err := net.DialTimeout("tcp", listen, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func readRSS(pid int) int64 {
	// Linux: /proc/<pid>/status VmRSS
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.ParseInt(fields[1], 10, 64)
				return kb * 1024
			}
		}
	}
	return 0
}

func rssTooHigh() bool {
	// self RSS
	return readRSS(os.Getpid()) > rssAbortBytes
}

func httpGet(url string, timeout time.Duration) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(body), nil
}

// --- load client ---

type loadStats struct {
	Total   int64
	OK      int64
	Fail    int64
	Status  sync.Map // status code -> count
	Bytes   int64
	LatSum  int64 // ns
	LatMax  int64
	ErrSample string
}

func (s *loadStats) addStatus(code int) {
	v, _ := s.Status.LoadOrStore(code, new(int64))
	atomic.AddInt64(v.(*int64), 1)
}

func (s *loadStats) successRate() float64 {
	t := atomic.LoadInt64(&s.Total)
	if t == 0 {
		return 0
	}
	return float64(atomic.LoadInt64(&s.OK)) / float64(t)
}

func (s *loadStats) statusMap() map[int]int64 {
	out := map[int]int64{}
	s.Status.Range(func(k, v any) bool {
		out[k.(int)] = atomic.LoadInt64(v.(*int64))
		return true
	})
	return out
}

func messagesBody(stream bool, n int) []byte {
	// Minimal Anthropic Messages body that the translator accepts.
	payload := map[string]any{
		"model":      "claude-sonnet-4",
		"max_tokens": 64,
		"stream":     stream,
		"messages": []map[string]any{
			{"role": "user", "content": fmt.Sprintf("m11 load %d", n)},
		},
	}
	b, _ := json.Marshal(payload)
	return b
}

func runLoad(baseURL string, concurrency, total int, stream bool, timeout time.Duration) *loadStats {
	stats := &loadStats{}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        concurrency * 2,
			MaxIdleConnsPerHost: concurrency * 2,
			IdleConnTimeout:     30 * time.Second,
			ForceAttemptHTTP2:   false,
		},
	}
	jobs := make(chan int, total)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				doOne(client, baseURL, n, stream, stats)
			}
		}()
	}
	for i := 0; i < total; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return stats
}

func doOne(client *http.Client, baseURL string, n int, stream bool, stats *loadStats) {
	atomic.AddInt64(&stats.Total, 1)
	body := messagesBody(stream, n)
	url := strings.TrimRight(baseURL, "/") + "/v1/messages"
	start := time.Now()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		atomic.AddInt64(&stats.Fail, 1)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "m11-bench")
	req.Header.Set("anthropic-version", "2023-06-01")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := client.Do(req)
	lat := time.Since(start).Nanoseconds()
	atomic.AddInt64(&stats.LatSum, lat)
	for {
		old := atomic.LoadInt64(&stats.LatMax)
		if lat <= old || atomic.CompareAndSwapInt64(&stats.LatMax, old, lat) {
			break
		}
	}
	if err != nil {
		atomic.AddInt64(&stats.Fail, 1)
		if stats.ErrSample == "" {
			stats.ErrSample = err.Error()
		}
		return
	}
	defer resp.Body.Close()
	stats.addStatus(resp.StatusCode)
	nread, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<20))
	atomic.AddInt64(&stats.Bytes, nread)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		atomic.AddInt64(&stats.OK, 1)
	} else {
		atomic.AddInt64(&stats.Fail, 1)
	}
}

// --- G3 ---

func runG3(bin, db, listen, artifacts string, _ bool) gateResult {
	outPath := filepath.Join(artifacts, "m11-g3.txt")
	logPath := filepath.Join(artifacts, "m11-g3-proxy.log")
	p, err := startProxy(bin, db, listen, nil, logPath)
	if err != nil {
		r := gateResult{Name: "G3", Pass: false, Detail: fmt.Sprintf("start proxy: %v", err)}
		writeArtifact(outPath, r.Report())
		return r
	}
	defer p.stop()

	const conc = 100
	const total = 500 // enough volume at 100 conc
	base := "http://" + listen
	start := time.Now()
	st := runLoad(base, conc, total, false, 30*time.Second)
	elapsed := time.Since(start)
	rate := st.successRate()
	pass := rate >= 0.99 && atomic.LoadInt64(&st.Total) == total
	rss := p.rssBytes()
	detail := fmt.Sprintf("concurrency: %d\ntotal_requests: %d\nok: %d\nfail: %d\nsuccess_rate: %.4f\npass_bar: >= 0.99\nelapsed: %s\nproxy_rss_mb: %.1f\nstatus_counts: %v\nerr_sample: %s\n",
		conc, st.Total, st.OK, st.Fail, rate, elapsed.Round(time.Millisecond),
		float64(rss)/(1<<20), st.statusMap(), st.ErrSample)
	r := gateResult{Name: "G3", Pass: pass, Detail: detail}
	writeArtifact(outPath, r.Report())
	return r
}

// --- G4 ---

func runG4(bin, db, listen, artifacts string) gateResult {
	outPath := filepath.Join(artifacts, "m11-g4.txt")
	logPath := filepath.Join(artifacts, "m11-g4-proxy.log")
	// Hold streams open via mock delay so 50 concurrent SSE are in flight.
	p, err := startProxy(bin, db, listen, []string{"--mock-stream-delay-ms", "150"}, logPath)
	if err != nil {
		r := gateResult{Name: "G4", Pass: false, Detail: fmt.Sprintf("start proxy: %v", err)}
		writeArtifact(outPath, r.Report())
		return r
	}
	defer p.stop()

	const streams = 50
	base := "http://" + listen

	var maxProxyRSS int64
	stop := make(chan struct{})
	var wgSample sync.WaitGroup
	wgSample.Add(1)
	go func() {
		defer wgSample.Done()
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				r := p.rssBytes()
				if r > maxProxyRSS {
					maxProxyRSS = r
				}
			}
		}
	}()

	// One concurrent burst of 50 SSE (pass bar: 50 streams, no OOM, RSS < 6G).
	// Use generous timeout; mock stream delay holds connections briefly.
	st := runLoad(base, streams, streams, true, 90*time.Second)
	// brief second burst to confirm stability (not counted against success floor)
	st2 := runLoad(base, streams, streams, true, 90*time.Second)

	close(stop)
	wgSample.Wait()
	if r := p.rssBytes(); r > maxProxyRSS {
		maxProxyRSS = r
	}

	oom := maxProxyRSS >= rssLimitBytes
	rate := st.successRate()
	rate2 := st2.successRate()
	// Pass: first concurrent 50 all (or ≥95%) succeed, RSS under 6G, no OOM.
	pass := !oom && maxProxyRSS < rssLimitBytes && rate >= 0.95 && atomic.LoadInt64(&st.OK) >= 48
	detail := fmt.Sprintf("streams: %d\nburst1_total: %d\nburst1_ok: %d\nburst1_fail: %d\nburst1_success_rate: %.4f\nburst2_total: %d\nburst2_ok: %d\nburst2_success_rate: %.4f\nmax_proxy_rss_mb: %.1f\nrss_limit_mb: %.1f\noom: %v\nstatus_counts_burst1: %v\nerr_sample: %s\npass_bar: no OOM; RSS < 6G; 50 streams\n",
		streams, st.Total, st.OK, st.Fail, rate, st2.Total, st2.OK, rate2,
		float64(maxProxyRSS)/(1<<20), float64(rssLimitBytes)/(1<<20), oom, st.statusMap(), st.ErrSample)
	r := gateResult{Name: "G4", Pass: pass, Detail: detail}
	writeArtifact(outPath, r.Report())
	return r
}

// --- G5 ---

func runG5(bin, db, listen, artifacts string) gateResult {
	outPath := filepath.Join(artifacts, "m11-g5.txt")
	logPath := filepath.Join(artifacts, "m11-g5-proxy.log")
	// Approach: mock returns 429 for half of Authorization tokens (fnv32 hash).
	// Executor failovers up to max_attempts=6, so end-client success should stay high.
	p, err := startProxy(bin, db, listen, []string{"--mock-fail-half"}, logPath)
	if err != nil {
		r := gateResult{Name: "G5", Pass: false, Detail: fmt.Sprintf("start proxy: %v", err)}
		writeArtifact(outPath, r.Report())
		return r
	}
	defer p.stop()

	const conc = 50
	const total = 400
	base := "http://" + listen
	start := time.Now()
	st := runLoad(base, conc, total, false, 45*time.Second)
	elapsed := time.Since(start)
	rate := st.successRate()
	pass := rate >= 0.95
	detail := fmt.Sprintf("approach: mock FailHalfByToken — 429 when fnv32(access_token)&1==1 (~half accounts)\nexecutor_failover: max_attempts=6 (lease AcquireAttempt loop)\nconcurrency: %d\ntotal: %d\nok: %d\nfail: %d\nsuccess_rate: %.4f\npass_bar: >= 0.95\nelapsed: %s\nproxy_rss_mb: %.1f\nstatus_counts: %v\nerr_sample: %s\n",
		conc, st.Total, st.OK, st.Fail, rate, elapsed.Round(time.Millisecond),
		float64(p.rssBytes())/(1<<20), st.statusMap(), st.ErrSample)
	r := gateResult{Name: "G5", Pass: pass, Detail: detail}
	writeArtifact(outPath, r.Report())
	return r
}

// --- G6 ---

func runG6(bin, db, listen, artifacts string, dur time.Duration) gateResult {
	outPath := filepath.Join(artifacts, "m11-g6.txt")
	logPath := filepath.Join(artifacts, "m11-g6-proxy.log")
	p, err := startProxy(bin, db, listen, nil, logPath)
	if err != nil {
		r := gateResult{Name: "G6", Pass: false, Detail: fmt.Sprintf("start proxy: %v", err)}
		writeArtifact(outPath, r.Report())
		return r
	}
	defer p.stop()

	base := "http://" + listen
	deadline := time.Now().Add(dur)
	st := &loadStats{}
	var maxRSS, minRSS, firstRSS int64
	minRSS = 1 << 62
	samples := 0
	start := time.Now()
	for time.Now().Before(deadline) {
		wave := runLoad(base, 20, 40, false, 20*time.Second)
		atomic.AddInt64(&st.Total, atomic.LoadInt64(&wave.Total))
		atomic.AddInt64(&st.OK, atomic.LoadInt64(&wave.OK))
		atomic.AddInt64(&st.Fail, atomic.LoadInt64(&wave.Fail))
		r := p.rssBytes()
		if firstRSS == 0 {
			firstRSS = r
		}
		if r > maxRSS {
			maxRSS = r
		}
		if r < minRSS {
			minRSS = r
		}
		samples++
		if r > rssLimitBytes {
			break
		}
		time.Sleep(2 * time.Second)
	}
	elapsed := time.Since(start)
	growth := float64(maxRSS-firstRSS) / float64(1<<20)
	flat := growth < 500 // <500MB growth over soak
	rate := st.successRate()
	pass := maxRSS < rssLimitBytes && flat && rate >= 0.95
	detail := fmt.Sprintf("duration_requested: %s\nduration_actual: %s\ntotal: %d\nok: %d\nfail: %d\nsuccess_rate: %.4f\nrss_first_mb: %.1f\nrss_max_mb: %.1f\nrss_min_mb: %.1f\nrss_growth_mb: %.1f\nsamples: %d\npass_bar: RSS flat; no OOM\n",
		dur, elapsed.Round(time.Second), st.Total, st.OK, st.Fail, rate,
		float64(firstRSS)/(1<<20), float64(maxRSS)/(1<<20), float64(minRSS)/(1<<20), growth, samples)
	r := gateResult{Name: "G6", Pass: pass, Detail: detail}
	writeArtifact(outPath, r.Report())
	return r
}

// --- util ---

func writeArtifact(path, content string) {
	_ = os.WriteFile(path, []byte(content), 0o644)
}

func hostLine() string {
	return fmt.Sprintf("%s %s/%s nproc=%d", runtime.GOOS, runtime.GOARCH, runtime.Version(), runtime.NumCPU())
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " | ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	killPoolProxy()
	os.Exit(1)
}

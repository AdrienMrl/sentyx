// Command teslcam-bench runs the sentry-analysis benchmark: it harvests real
// events off a deployed server into a labeled dataset, replays that dataset
// through the analyzer, and scores the verdicts against the labels.
//
//	teslcam-bench remote-list -ssh-host vps -remote-data-dir /var/lib/teslcam
//	teslcam-bench fetch       -ssh-host vps -remote-data-dir /var/lib/teslcam -event sentyx:2026-08-13_19-14-44
//	teslcam-bench list
//	teslcam-bench label                                # web UI for labeling
//	teslcam-bench run                                  # production's config
//	teslcam-bench run -model gemini-3.6-flash -fps 1    # a cheaper variant
//	teslcam-bench report bench/results/<run>.json
//
// See bench/README.md for the labeling workflow.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/AdrienMrl/teslcam/internal/bench"
	"github.com/AdrienMrl/teslcam/internal/gemini"
	"github.com/AdrienMrl/teslcam/internal/server"
)

const usage = `teslcam-bench <command> [flags]

commands:
  remote-list   list recent events on a deployed server
  fetch         copy one event's clips into the dataset as an unlabeled case
  add           register local clip files as an unlabeled case
  list          show the dataset and which cases still need labels
  label         open the web labeling UI
  run           analyze every labeled case and score the verdicts
  report        re-render (and re-score) a stored run

run "<command> -h" for a command's flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "remote-list":
		err = cmdRemoteList(ctx, os.Args[2:])
	case "fetch":
		err = cmdFetch(ctx, os.Args[2:])
	case "add":
		err = cmdAdd(os.Args[2:])
	case "list":
		err = cmdList(os.Args[2:])
	case "label":
		err = cmdLabel(ctx, os.Args[2:])
	case "run":
		err = cmdRun(ctx, os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "teslcam-bench: %v\n", err)
		os.Exit(1)
	}
}

// paths are where the benchmark keeps its three kinds of state: labels (in
// git), clips (large, out of git), and past runs (out of git).
type paths struct {
	dataset string
	clips   string
	results string
}

func (p *paths) bind(fs *flag.FlagSet) {
	fs.StringVar(&p.dataset, "dataset", "bench/dataset.json", "labeled dataset file")
	fs.StringVar(&p.clips, "clips-dir", "bench/clips", "directory holding each case's clips")
	fs.StringVar(&p.results, "results-dir", "bench/results", "directory for stored runs")
}

// remoteFlags carry no defaults: which box the footage comes from decides what
// the benchmark measures, so it is always stated explicitly.
type remoteFlags struct {
	host    string
	dataDir string
}

func (r *remoteFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&r.host, "ssh-host", "", "ssh destination of the server (required, e.g. vps)")
	fs.StringVar(&r.dataDir, "remote-data-dir", "", "server data directory on that host (required, e.g. /var/lib/teslcam)")
}

func (r *remoteFlags) remote() (bench.Remote, error) {
	if r.host == "" {
		return bench.Remote{}, errors.New("-ssh-host is required")
	}
	if r.dataDir == "" {
		return bench.Remote{}, errors.New("-remote-data-dir is required")
	}
	return bench.Remote{Host: r.host, DataDir: r.dataDir}, nil
}

func cmdRemoteList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("remote-list", flag.ExitOnError)
	var rf remoteFlags
	rf.bind(fs)
	limit := fs.Int("limit", 20, "how many recent events to list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	remote, err := rf.remote()
	if err != nil {
		return err
	}
	events, err := remote.ListEvents(ctx, *limit)
	if err != nil {
		return err
	}
	for _, ev := range events {
		fmt.Printf("%-32s %-12s %-30s %s\n", ev.ID, orDash(ev.ThreatLevel), orDash(ev.Reason), orDash(ev.City))
	}
	fmt.Printf("\n%d events. Add one: teslcam-bench fetch -ssh-host %s -remote-data-dir %s -event <id>\n",
		len(events), remote.Host, remote.DataDir)
	return nil
}

func cmdFetch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	var p paths
	var rf remoteFlags
	p.bind(fs)
	rf.bind(fs)
	eventID := fs.String("event", "", "event id to fetch (required)")
	caseID := fs.String("case-id", "", "case id to create (default: derived from the event id)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *eventID == "" {
		return errors.New("-event is required")
	}
	remote, err := rf.remote()
	if err != nil {
		return err
	}

	ds, err := loadOrInit(p.dataset)
	if err != nil {
		return err
	}
	id := *caseID
	if id == "" {
		id = slugify(*eventID)
	}
	if _, exists := ds.Find(id); exists {
		return fmt.Errorf("case %q already exists in %s", id, p.dataset)
	}

	ev, err := remote.Event(ctx, *eventID)
	if err != nil {
		return err
	}
	fmt.Printf("fetching %d clips of %s into %s/%s\n", len(ev.Files), ev.ID, p.clips, id)
	clips, err := remote.Download(ctx, ev, filepath.Join(p.clips, id))
	if err != nil {
		return err
	}
	// Every camera is downloaded so all of them can be watched while labeling,
	// but the case analyzes exactly one: the benchmark never shows the model
	// more than one video at a time.
	ds.Cases = append(ds.Cases, bench.NewCase(id, remote, ev, clips[:1]))
	if err := bench.SaveDataset(p.dataset, ds); err != nil {
		return err
	}

	fmt.Printf("added unlabeled case %q: analyzing %s (%d other camera(s) downloaded for viewing)\n",
		id, clips[0], len(clips)-1)
	if len(ev.AnalysisJSON) > 0 {
		fmt.Printf("production said (reference only, not ground truth): %s\n", ev.AnalysisJSON)
	}
	fmt.Printf("\nwatch the clips, then fill in \"label\" for %q in %s:\n", id, p.dataset)
	fmt.Println(`  {"contact":false,"threat":"none|low|high","start_seconds":null,"end_seconds":null}`)
	return nil
}

// cmdAdd registers footage that never went through the server — a downloaded
// clip, a staged test in the driveway. The files are copied in, so the dataset
// does not depend on wherever they happened to be sitting.
func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	var p paths
	p.bind(fs)
	caseID := fs.String("case-id", "", "case id to create (required)")
	origin := fs.String("origin", "", "where the footage came from, recorded with the case")
	notes := fs.String("notes", "", "free-form context stored on the case")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *caseID == "" {
		return errors.New("-case-id is required")
	}
	if fs.NArg() != 1 {
		return errors.New("give exactly one clip file: the benchmark sends the model one video per case")
	}
	ds, err := loadOrInit(p.dataset)
	if err != nil {
		return err
	}
	if _, exists := ds.Find(*caseID); exists {
		return fmt.Errorf("case %q already exists in %s", *caseID, p.dataset)
	}

	destDir := filepath.Join(p.clips, *caseID)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	var clips []string
	for _, src := range fs.Args() {
		name := filepath.Base(src)
		if err := copyFile(src, filepath.Join(destDir, name)); err != nil {
			return err
		}
		clips = append(clips, name)
	}
	ds.Cases = append(ds.Cases, bench.Case{
		ID:     *caseID,
		Clips:  clips,
		Source: bench.Source{Origin: *origin},
		Notes:  *notes,
	})
	if err := bench.SaveDataset(p.dataset, ds); err != nil {
		return err
	}
	fmt.Printf("added unlabeled case %q with %d clip(s) in %s\n", *caseID, len(clips), destDir)
	return nil
}

// copyFile copies src to dest, refusing to clobber an existing clip.
func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	var p paths
	p.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ds, err := loadOrInit(p.dataset)
	if err != nil {
		return err
	}
	if len(ds.Cases) == 0 {
		fmt.Printf("%s has no cases yet — add one with teslcam-bench fetch\n", p.dataset)
		return nil
	}
	labeled := 0
	for i := range ds.Cases {
		c := &ds.Cases[i]
		status := "UNLABELED"
		if c.Labeled() {
			labeled++
			status = c.Label.Threat
			if c.Label.Contact {
				status += "+contact"
			}
		}
		fmt.Printf("%-34s %-16s %d clips\n", c.ID, status, len(c.Clips))
	}
	fmt.Printf("\n%d cases, %d labeled, %d to go\n", len(ds.Cases), labeled, len(ds.Cases)-labeled)
	return nil
}

// cmdLabel serves the labeling UI. It binds to loopback and has no auth: it
// edits a file in the working tree and plays footage from it, so it is a local
// tool by construction.
func cmdLabel(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("label", flag.ExitOnError)
	var p paths
	p.bind(fs)
	addr := fs.String("addr", "127.0.0.1:8099", "address to listen on")
	open := fs.Bool("open", true, "open the page in a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := os.Stat(p.dataset); err != nil {
		return fmt.Errorf("%s: %w (fetch or add a case first)", p.dataset, err)
	}
	editor := &bench.Editor{DatasetPath: p.dataset, ClipRoot: p.clips, ResultsDir: p.results}
	srv := &http.Server{Addr: *addr, Handler: editor.Handler()}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	url := "http://" + ln.Addr().String()
	fmt.Printf("labeling %s at %s  (ctrl-c to stop)\n", p.dataset, url)
	if *open {
		exec.Command("open", url).Run()
	}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var p paths
	p.bind(fs)
	// The verdict-affecting flags default to what production runs today, so a
	// bare "run" measures the shipping configuration. They are not implicit:
	// every one is written into the run file and printed at the top of the
	// report, so a stored result always states the configuration it came from.
	model := fs.String("model", "gemini-3.7-flash", "Gemini model")
	mediaRes := fs.String("media-resolution", "medium", "media resolution: low, medium or high")
	fps := fs.Int("fps", 3, "video sampling rate in frames per second")
	analyzerCmd := fs.String("analyzer", "", "external analyzer command to benchmark instead of the Gemini client;\n"+
		"clip paths are appended (best-ranked first) and it must print one JSON verdict to stdout")
	repeat := fs.Int("repeat", 1, "analyses per case; above 1 measures run-to-run stability")
	parallel := fs.Int("parallel", 2, "concurrent analyses")
	timeout := fs.Duration("timeout", 10*time.Minute, "timeout for one analysis")
	notes := fs.String("notes", "", "what this run is testing, stored with the results")
	only := fs.String("case", "", "comma-separated case ids or tags to run (default: all labeled)")
	dryRun := fs.Bool("dry-run", false, "list what would be analyzed and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The Gemini flags carry defaults, so "was it set?" cannot be read off the
	// value — an external analyzer run has to reject them explicitly rather
	// than silently record a Gemini config it never used.
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if *analyzerCmd != "" {
		for _, name := range []string{"model", "media-resolution", "fps"} {
			if set[name] {
				return fmt.Errorf("-%s configures the Gemini client and cannot be combined with -analyzer", name)
			}
		}
		*model, *mediaRes, *fps = "", "", 0
	} else {
		if *model == "" {
			return errors.New("-model must not be empty")
		}
		if *mediaRes == "" {
			return errors.New("-media-resolution must not be empty (low, medium or high)")
		}
		if *fps <= 0 {
			return errors.New("-fps must be positive")
		}
	}

	ds, err := bench.LoadDataset(p.dataset)
	if err != nil {
		return err
	}
	var filters []string
	if *only != "" {
		filters = strings.Split(*only, ",")
	}
	cases, unlabeled := ds.Select(filters)
	for _, c := range unlabeled {
		fmt.Fprintf(os.Stderr, "skipping unlabeled case %s\n", c.ID)
	}
	if len(cases) == 0 {
		return errors.New("no labeled cases to run")
	}

	if *dryRun {
		for _, c := range cases {
			clip, err := c.ClipPath(p.clips)
			if err != nil {
				return err
			}
			fmt.Printf("%-34s want=%-6s %s\n", c.ID, c.Label.Threat, filepath.Base(clip))
		}
		fmt.Printf("\n%d cases x %d repeats = %d analyses\n", len(cases), *repeat, len(cases)**repeat)
		return nil
	}

	analyzer, analyzerName, err := buildAnalyzer(*model, *mediaRes, *fps, *analyzerCmd)
	if err != nil {
		return err
	}

	commit, dirty := gitRevision()
	done := 0
	total := len(cases) * *repeat
	runner := &bench.Runner{
		Analyzer: analyzer,
		ClipRoot: p.clips,
		Parallel: *parallel,
		Timeout:  *timeout,
		Config: bench.RunConfig{
			Analyzer:        analyzerName,
			Model:           *model,
			MediaResolution: *mediaRes,
			FPS:             *fps,
			Repeat:          *repeat,
			Commit:          commit,
			Dirty:           dirty,
			Notes:           *notes,
		},
		Progress: func(res bench.Result) {
			done++
			status := "ok"
			if res.Error != "" {
				status = "ERROR: " + truncate(res.Error, 120)
			} else if res.Verdict != nil {
				status = res.Verdict.Threat
			}
			fmt.Fprintf(os.Stderr, "[%d/%d] %s %s (%.1fs)\n", done, total, res.CaseID, status,
				float64(res.DurationMS)/1000)
		},
	}
	run, err := runner.Execute(ctx, cases)
	if err != nil {
		return err
	}
	path, err := bench.SaveRun(p.results, run)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nsaved %s\n\n", path)
	return bench.WriteReport(os.Stdout, run, ds, run.Score(ds))
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	var p paths
	p.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ds, err := bench.LoadDataset(p.dataset)
	if err != nil {
		return err
	}
	path := fs.Arg(0)
	if path == "" {
		if path, err = latestRun(p.results); err != nil {
			return err
		}
	}
	run, err := bench.LoadRun(path)
	if err != nil {
		return err
	}
	return bench.WriteReport(os.Stdout, run, ds, run.Score(ds))
}

// buildAnalyzer returns the analyzer under test and the name recorded with the
// results. The runner only needs a server.Analyzer, so anything that satisfies
// that interface — a Go client for another provider, or a script wrapping one —
// can be benchmarked against the same dataset and scorer.
func buildAnalyzer(model, mediaRes string, fps int, analyzerCmd string) (server.Analyzer, string, error) {
	if analyzerCmd != "" {
		a, err := server.NewCmdAnalyzer(strings.Fields(analyzerCmd))
		return a, analyzerCmd, err
	}
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, "", errors.New("GEMINI_API_KEY is not set")
	}
	a, err := gemini.New(apiKey, model, mediaRes, fps)
	return a, "gemini", err
}

// loadOrInit reads the dataset, treating a missing file as an empty one so the
// first fetch works on a clean checkout.
func loadOrInit(path string) (*bench.Dataset, error) {
	ds, err := bench.LoadDataset(path)
	if errors.Is(err, os.ErrNotExist) {
		return &bench.Dataset{}, nil
	}
	return ds, err
}

func latestRun(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no runs in %s", dir)
	}
	// Run ids are UTC timestamps, so lexical order is chronological.
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1]), nil
}

// gitRevision records the code the run was made with; the prompt lives in the
// source tree, so an uncommitted tree makes a result unreproducible and says so.
func gitRevision() (commit string, dirty bool) {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "", false
	}
	commit = strings.TrimSpace(string(out))
	status, err := exec.Command("git", "status", "--porcelain").Output()
	return commit, err == nil && len(strings.TrimSpace(string(status))) > 0
}

var nonSlug = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func slugify(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(s, "-"), "-")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

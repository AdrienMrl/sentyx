package bench

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// WriteReport renders a run and its scores as plain text. The per-case table
// comes first: an aggregate rate is only ever a pointer to the case that
// regressed, and that case is what gets re-watched.
func WriteReport(w io.Writer, run *Run, ds *Dataset, scores []CaseScore) error {
	byKey := map[string]Result{}
	for _, res := range run.Results {
		byKey[resultKey(res.CaseID, res.Attempt)] = res
	}

	fmt.Fprintf(w, "run %s  %s\n", run.ID, run.StartedAt.Local().Format(time.RFC3339))
	fmt.Fprintf(w, "analyzer=%s", orNone(run.Config.Analyzer))
	if run.Config.Model != "" {
		fmt.Fprintf(w, " model=%s media=%s fps=%d",
			run.Config.Model, orNone(run.Config.MediaResolution), run.Config.FPS)
	}
	fmt.Fprintf(w, " repeat=%d", run.Config.Repeat)
	if run.Config.Commit != "" {
		dirty := ""
		if run.Config.Dirty {
			dirty = "-dirty"
		}
		fmt.Fprintf(w, " commit=%s%s", run.Config.Commit, dirty)
	}
	fmt.Fprintln(w)
	if run.Config.Notes != "" {
		fmt.Fprintf(w, "notes: %s\n", run.Config.Notes)
	}
	fmt.Fprintln(w)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CASE\tCONTACT\tTHREAT\tOUTCOME\tTIMING\tTOKENS\tSECS")
	for _, s := range scores {
		res := byKey[resultKey(s.CaseID, s.Attempt)]
		name := s.CaseID
		if run.Config.Repeat > 1 {
			name = fmt.Sprintf("%s#%d", s.CaseID, s.Attempt)
		}
		tokens := "-"
		if res.Usage != nil {
			tokens = fmt.Sprintf("%d", res.Usage.TotalTokens)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%.1f\n",
			name, contactCell(s), fmt.Sprintf("%s->%s", s.Want, orNone(s.Got)),
			outcome(s), timing(s), tokens, float64(res.DurationMS)/1000)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	sum := Summarize(scores)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%d cases, %d scored analyses", sum.Cases, sum.Attempts)
	if sum.Errors > 0 {
		fmt.Fprintf(w, ", %d errored", sum.Errors)
	}
	fmt.Fprintln(w)
	if sum.ContactScored > 0 {
		fmt.Fprintf(w, "CONTACT missed %s  (something touched the car, model said it did not)\n",
			rate(sum.ContactMisses, sum.ContactCases))
		fmt.Fprintf(w, "CONTACT phantom %s  (nothing touched the car, model said it did)\n",
			rate(sum.ContactFalseAlarms, sum.NoContactCases))
	} else if sum.Attempts > 0 {
		fmt.Fprintln(w, "CONTACT        unscored (the analyzer does not report a contact field)")
	}
	fmt.Fprintf(w, "exact threat   %s\n", rate(sum.Exact, sum.Attempts))
	fmt.Fprintf(w, "misses         %s  (real event called \"none\")\n", rate(sum.Misses, sum.ConcernCases))
	fmt.Fprintf(w, "false alarms   %s  (quiet event called a threat)\n", rate(sum.FalseAlarms, sum.CalmCases))
	if sum.TimingScored > 0 {
		fmt.Fprintf(w, "timing hits    %s  (window within %ds)\n", rate(sum.TimingHits, sum.TimingScored), timingSlackSeconds)
	}
	if len(sum.Unstable) > 0 {
		fmt.Fprintf(w, "unstable       %s  (repeats disagreed)\n", strings.Join(sum.Unstable, ", "))
	}

	usage, usd, reported := run.Cost()
	if reported > 0 {
		fmt.Fprintf(w, "tokens         %d in / %d out", usage.PromptTokens, usage.OutputTokens)
		if usd > 0 {
			fmt.Fprintf(w, "   est. $%.4f total, $%.4f per analysis", usd, usd/float64(reported))
		}
		fmt.Fprintln(w)
	}

	writeConfusion(w, sum)
	writeFailures(w, run, ds, scores)
	return nil
}

// writeConfusion prints want-vs-got counts, the quickest read on whether the
// analyzer is biased toward calling things quiet or loud.
func writeConfusion(w io.Writer, sum Summary) {
	if len(sum.Confusion) == 0 {
		return
	}
	fmt.Fprintln(w, "\nwant \\ got")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "\t%s\n", strings.Join(severities, "\t"))
	for _, want := range severities {
		row, ok := sum.Confusion[want]
		if !ok {
			continue
		}
		fmt.Fprintf(tw, "%s", want)
		for _, got := range severities {
			cell := "."
			if n := row[got]; n > 0 {
				cell = fmt.Sprintf("%d", n)
			}
			fmt.Fprintf(tw, "\t%s", cell)
		}
		fmt.Fprintln(tw)
	}
	tw.Flush()
}

// writeFailures spells out what the label said versus what the model said, for
// every attempt that did not match — the part worth reading before changing
// the prompt.
func writeFailures(w io.Writer, run *Run, ds *Dataset, scores []CaseScore) {
	var bad []CaseScore
	for _, s := range scores {
		if !s.Exact || s.Error != "" || s.ContactMiss || s.ContactFalseAlarm {
			bad = append(bad, s)
		}
	}
	if len(bad) == 0 {
		return
	}
	sort.SliceStable(bad, func(i, j int) bool { return severityOf(bad[i]) > severityOf(bad[j]) })
	fmt.Fprintln(w, "\ndisagreements")
	for _, s := range bad {
		fmt.Fprintf(w, "\n  %s  [%s]\n", s.CaseID, outcome(s))
		if c, ok := ds.Find(s.CaseID); ok && c.Labeled() {
			fmt.Fprintf(w, "    label:  contact=%t %s\n", c.Label.Contact, c.Label.Threat)
		}
		if res, ok := byResult(run, s); ok && res.Verdict != nil {
			fmt.Fprintf(w, "    model:  contact=%s %s — %s\n",
				boolCell(s.GotContact), orNone(s.Got), res.Verdict.Description)
		}
		if s.Error != "" {
			fmt.Fprintf(w, "    error:  %s\n", s.Error)
		}
	}
}

// severityOf ranks failures for display. A missed contact outranks every
// severity disagreement: mis-rating an event the model saw is a judgement
// call, not seeing it at all is the failure this benchmark is for.
func severityOf(s CaseScore) int {
	switch {
	case s.Error != "":
		return 5
	case s.ContactMiss:
		return 4
	case s.Miss:
		return 3
	case s.ContactFalseAlarm:
		return 2
	case s.FalseAlarm:
		return 1
	default:
		return 0
	}
}

func outcome(s CaseScore) string { return s.Outcome() }

// Outcome is the one-word verdict on an attempt, shared by the text report and
// the web UI so the two never disagree about what counts as a failure.
func (s CaseScore) Outcome() string {
	switch {
	case s.Error != "":
		return "ERROR"
	case s.ContactMiss:
		return "MISSED CONTACT"
	case s.Miss:
		return "MISS"
	case s.ContactFalseAlarm:
		return "PHANTOM CONTACT"
	case s.FalseAlarm:
		return "FALSE ALARM"
	case s.Exact:
		return "ok"
	case s.SeverityDelta > 0:
		return fmt.Sprintf("over +%d", s.SeverityDelta)
	default:
		return fmt.Sprintf("under %d", s.SeverityDelta)
	}
}

// contactCell shows the label and the verdict side by side, since the whole
// point of the run is whether the second tracks the first.
func contactCell(s CaseScore) string {
	return fmt.Sprintf("%s->%s", boolCell(&s.WantContact), boolCell(s.GotContact))
}

func boolCell(b *bool) string {
	switch {
	case b == nil:
		return "?"
	case *b:
		return "yes"
	default:
		return "no"
	}
}

// byResult finds the stored result an individual score came from.
func byResult(run *Run, s CaseScore) (Result, bool) {
	for _, res := range run.Results {
		if res.CaseID == s.CaseID && res.Attempt == s.Attempt {
			return res, true
		}
	}
	return Result{}, false
}

func timing(s CaseScore) string {
	if s.TimingHit == nil {
		return "-"
	}
	if *s.TimingHit {
		return "hit"
	}
	return "MISS"
}

func rate(n, total int) string {
	if total == 0 {
		return fmt.Sprintf("%d/0", n)
	}
	return fmt.Sprintf("%d/%d (%.0f%%)", n, total, 100*float64(n)/float64(total))
}

func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func resultKey(caseID string, attempt int) string {
	return fmt.Sprintf("%s#%d", caseID, attempt)
}

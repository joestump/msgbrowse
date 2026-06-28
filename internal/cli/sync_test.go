package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/joestump/msgbrowse/internal/config"
)

// recordingStage returns a stageFunc that appends its name to *order (proving the
// stage ran and in what position) and returns the given summary line + error.
func recordingStage(order *[]string, name, summary string, err error) stageFunc {
	return func(_ context.Context) (string, error) {
		*order = append(*order, name)
		return summary, err
	}
}

// allStagesDeps wires every stage to a recorder so tests can assert order and
// which stages ran. Pass nil for stages a test wants skipped.
func allStagesDeps(order *[]string) syncDeps {
	return syncDeps{
		export:       recordingStage(order, "export", "export:   done", nil),
		importSignal: recordingStage(order, "import-signal", "signal:   ok", nil),
		importIMsg:   recordingStage(order, "import-imessage", "imessage: ok", nil),
		media:        recordingStage(order, "media", "media:    ok", nil),
		embed:        recordingStage(order, "embed", "embed:    ok", nil),
		facts:        recordingStage(order, "facts", "facts:    ok", nil),
	}
}

func TestRunSyncStageOrder(t *testing.T) {
	var order []string
	out := &bytes.Buffer{}

	if err := runSync(context.Background(), out, allStagesDeps(&order), syncOptions{}); err != nil {
		t.Fatalf("runSync: %v", err)
	}
	want := []string{"export", "import-signal", "import-imessage", "media", "embed", "facts"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("stage order = %v, want %v", order, want)
	}
	// Every stage's summary line is printed.
	for _, line := range []string{"export:   done", "signal:   ok", "imessage: ok", "media:    ok", "embed:    ok", "facts:    ok"} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output %q missing %q", out.String(), line)
		}
	}
}

func TestRunSyncNilStagesAreSkippedInOrder(t *testing.T) {
	// A nil stage func (a --no-* flag, or an unset source root) is passed over,
	// while the surviving stages keep their relative order.
	var order []string
	deps := syncDeps{
		export:       recordingStage(&order, "export", "x", nil),
		importSignal: nil, // unset Signal root
		importIMsg:   recordingStage(&order, "import-imessage", "x", nil),
		media:        nil, // --no-media
		embed:        recordingStage(&order, "embed", "x", nil),
		facts:        nil, // --no-facts
	}
	if err := runSync(context.Background(), &bytes.Buffer{}, deps, syncOptions{}); err != nil {
		t.Fatalf("runSync: %v", err)
	}
	want := []string{"export", "import-imessage", "embed"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("ran stages = %v, want %v", order, want)
	}
}

func TestRunSyncHardFailureAbortsWithoutSkip(t *testing.T) {
	// media fails; without --skip-on-error the pipeline aborts there and embed/facts
	// never run. The error names the failing stage.
	var order []string
	deps := allStagesDeps(&order)
	deps.media = recordingStage(&order, "media", "", errors.New("boom"))

	err := runSync(context.Background(), &bytes.Buffer{}, deps, syncOptions{skipOnError: false})
	if err == nil {
		t.Fatalf("expected error when a hard stage fails without --skip-on-error")
	}
	if !strings.Contains(err.Error(), "media") {
		t.Errorf("error %q should name the failing stage", err.Error())
	}
	// Stages after media must NOT have run.
	for _, after := range []string{"embed", "facts"} {
		for _, ran := range order {
			if ran == after {
				t.Errorf("%s ran despite media failing without --skip-on-error; order=%v", after, order)
			}
		}
	}
}

func TestRunSyncSkipOnErrorContinuesPastHardFailure(t *testing.T) {
	// import-signal fails; with --skip-on-error the pipeline continues through the
	// rest and then exits non-zero (errSyncFailures) because a hard stage failed.
	var order []string
	deps := allStagesDeps(&order)
	deps.importSignal = recordingStage(&order, "import-signal", "", errors.New("boom"))

	err := runSync(context.Background(), &bytes.Buffer{}, deps, syncOptions{skipOnError: true})
	if err == nil {
		t.Fatalf("expected non-zero (errSyncFailures) when a hard stage failed under --skip-on-error")
	}
	if !errors.Is(err, errSyncFailures) {
		t.Errorf("error %v should wrap errSyncFailures", err)
	}
	// The remaining stages must have run despite the early failure.
	for _, after := range []string{"import-imessage", "media", "embed", "facts"} {
		found := false
		for _, ran := range order {
			if ran == after {
				found = true
			}
		}
		if !found {
			t.Errorf("%s did not run after import-signal failed under --skip-on-error; order=%v", after, order)
		}
	}
}

func TestRunSyncLLMStageFailuresAreWarnedNotFailed(t *testing.T) {
	// embed and facts (LLM-dependent) fail — e.g. no reachable endpoint. They must
	// be warned-and-skipped REGARDLESS of --skip-on-error, and the run exits
	// SUCCESS (a fully-local run with no LLM still completes export/import/media).
	for _, skipOnError := range []bool{false, true} {
		var order []string
		deps := allStagesDeps(&order)
		deps.embed = recordingStage(&order, "embed", "", errors.New("connection refused"))
		deps.facts = recordingStage(&order, "facts", "", errors.New("connection refused"))

		out := &bytes.Buffer{}
		err := runSync(context.Background(), out, deps, syncOptions{skipOnError: skipOnError})
		if err != nil {
			t.Errorf("skipOnError=%v: LLM-stage failures must NOT fail the run, got %v", skipOnError, err)
		}
		// Both LLM stages were still attempted (so the warning path is exercised).
		for _, want := range []string{"embed", "facts"} {
			found := false
			for _, ran := range order {
				if ran == want {
					found = true
				}
			}
			if !found {
				t.Errorf("skipOnError=%v: %s stage was not attempted; order=%v", skipOnError, want, order)
			}
		}
		// The hard stages still printed their summaries.
		if !strings.Contains(out.String(), "media:    ok") {
			t.Errorf("skipOnError=%v: expected media summary, got %q", skipOnError, out.String())
		}
	}
}

func TestRunSyncEmptyPipelineSucceeds(t *testing.T) {
	// Everything skipped (no roots, all --no-*): a no-op pipeline is success.
	if err := runSync(context.Background(), &bytes.Buffer{}, syncDeps{}, syncOptions{}); err != nil {
		t.Errorf("empty pipeline should succeed, got %v", err)
	}
}

func TestSyncDepsFromConfigSkipsDisabledStages(t *testing.T) {
	// Production wiring: each --no-* flag (and an unset source root) wires its stage
	// as nil so runSync skips it. We assert nil-ness only; the funcs are never
	// invoked, so a nil store is fine here.
	cfg := &config.Config{ArchiveRoot: "/arch"} // iMessage root unset → importIMsg nil
	w := syncWiring{noExport: true, noMedia: true, noEmbed: true, noFacts: true}

	deps := syncDepsFromConfig(nil, cfg, &bytes.Buffer{}, w)

	if deps.export != nil {
		t.Error("--no-export should leave export nil")
	}
	if deps.media != nil {
		t.Error("--no-media should leave media nil")
	}
	if deps.embed != nil {
		t.Error("--no-embed should leave embed nil")
	}
	if deps.facts != nil {
		t.Error("--no-facts should leave facts nil")
	}
	if deps.importSignal == nil {
		t.Error("import-signal should be wired when archive_root is set")
	}
	if deps.importIMsg != nil {
		t.Error("import-imessage should be nil when imessage_archive_root is unset")
	}
}

func TestSyncDepsFromConfigWiresEnabledStages(t *testing.T) {
	// With no skip flags and both roots set, every stage is wired.
	cfg := &config.Config{ArchiveRoot: "/arch", IMessageArchiveRoot: "/imsg"}
	deps := syncDepsFromConfig(nil, cfg, &bytes.Buffer{}, syncWiring{})

	for name, fn := range map[string]stageFunc{
		"export":          deps.export,
		"import-signal":   deps.importSignal,
		"import-imessage": deps.importIMsg,
		"media":           deps.media,
		"embed":           deps.embed,
		"facts":           deps.facts,
	} {
		if fn == nil {
			t.Errorf("stage %q should be wired but is nil", name)
		}
	}
}

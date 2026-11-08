package cli

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mehdi-shokohi/cppm/internal/manifest"
	"github.com/mehdi-shokohi/cppm/internal/project"
	"github.com/mehdi-shokohi/cppm/internal/safety"
	"github.com/mehdi-shokohi/cppm/internal/toolchain"
)

// Everything after `--` belongs to the user's program, including arguments that
// look like cppm's own flags. Getting this wrong means `cppm run -- --release`
// silently builds the release preset instead of passing --release along.
func TestProgramArgumentsAreSplitAtTheSeparator(t *testing.T) {
	cases := []struct {
		args     []string
		wantCppm []string
		wantProg []string
		wantSep  bool
	}{
		{nil, nil, nil, false},
		{[]string{"--release"}, []string{"--release"}, nil, false},
		{[]string{"--"}, []string{}, []string{}, true},
		{[]string{"--", "--release"}, []string{}, []string{"--release"}, true},
		{[]string{"--sanitize=asan", "--", "-v", "x"}, []string{"--sanitize=asan"}, []string{"-v", "x"}, true},
		// A second `--` is the program's, not cppm's: only the first one is a
		// separator.
		{[]string{"--", "a", "--", "b"}, []string{}, []string{"a", "--", "b"}, true},
	}
	for _, c := range cases {
		gotCppm, gotProg, gotSep := splitProgramArgs(c.args)
		if !reflect.DeepEqual(gotCppm, c.wantCppm) || !reflect.DeepEqual(gotProg, c.wantProg) || gotSep != c.wantSep {
			t.Errorf("splitProgramArgs(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.args, gotCppm, gotProg, gotSep, c.wantCppm, c.wantProg, c.wantSep)
		}
	}
}

// A program argument beginning with '-' reaches the program unchanged. cppm
// rejects leading-dash values elsewhere -- a git ref, a ctest filter -- because
// they would be read as options by the program being driven. Here the opposite
// is required: argv is passed to exec.Command as a slice, so the program's own
// options are its business.
func TestProgramArgumentsKeepTheirLeadingDashes(t *testing.T) {
	_, prog, _ := splitProgramArgs([]string{"--", "--upload-pack=evil", "-rf", "/"})
	want := []string{"--upload-pack=evil", "-rf", "/"}
	if !reflect.DeepEqual(prog, want) {
		t.Errorf("program args = %q, want %q", prog, want)
	}
}

func execManifest(targets map[string]manifest.Target) *manifest.Manifest {
	m := manifest.New("Demo", 20)
	m.Targets = targets
	return m
}

func TestRunTargetPicksTheOnlyExecutable(t *testing.T) {
	m := execManifest(map[string]manifest.Target{
		"Demo":  {Kind: manifest.KindExecutable, Sources: []string{"src/main.cpp"}},
		"mylib": {Kind: manifest.KindLibrary, Sources: []string{"src/lib.cpp"}},
	})
	got, err := runTargetOf(m, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Demo" {
		t.Errorf("target = %q, want Demo", got)
	}
}

// A test executable is never picked automatically, for the same reason cppm
// never infers that a target called "foo_tests" is a test: being wrong runs the
// wrong binary, and under valgrind that is a wasted half hour.
func TestRunTargetNeverPicksATestTargetOnItsOwn(t *testing.T) {
	m := execManifest(map[string]manifest.Target{
		"Demo_tests": {Kind: manifest.KindExecutable, Sources: []string{"tests/t.cpp"}, Test: true},
	})
	_, err := runTargetOf(m, "")
	if err == nil {
		t.Fatal("a project whose only executable is a test target must not run it implicitly")
	}
	for _, want := range []string{"cppm test", "--target Demo_tests"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q: %v", want, err)
		}
	}

	// Named explicitly, it is allowed: it is an executable, and running it
	// under a sanitizer by hand is a reasonable thing to want.
	if got, err := runTargetOf(m, "Demo_tests"); err != nil || got != "Demo_tests" {
		t.Errorf("an explicitly named test target should be runnable: %q, %v", got, err)
	}
}

func TestRunTargetDeclinesToChooseBetweenExecutables(t *testing.T) {
	m := execManifest(map[string]manifest.Target{
		"Demo":  {Kind: manifest.KindExecutable, Sources: []string{"src/main.cpp"}},
		"tools": {Kind: manifest.KindExecutable, Sources: []string{"src/tools.cpp"}},
	})
	_, err := runTargetOf(m, "")
	if err == nil {
		t.Fatal("two executables must not be resolved silently")
	}
	// Both names have to appear, or the user cannot act on the message.
	for _, want := range []string{"Demo", "tools", "--target"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q: %v", want, err)
		}
	}
}

func TestRunTargetRefusesALibrary(t *testing.T) {
	m := execManifest(map[string]manifest.Target{
		"Demo":  {Kind: manifest.KindExecutable, Sources: []string{"src/main.cpp"}},
		"mylib": {Kind: manifest.KindLibrary, Sources: []string{"src/lib.cpp"}},
	})
	_, err := runTargetOf(m, "mylib")
	if err == nil {
		t.Fatal("a library target must not be runnable")
	}
	if !strings.Contains(err.Error(), "Demo") {
		t.Errorf("the refusal should name an executable the user could run instead: %v", err)
	}
}

func TestRunTargetNamesWhatExistsForAnUnknownTarget(t *testing.T) {
	m := execManifest(map[string]manifest.Target{
		"Demo": {Kind: manifest.KindExecutable, Sources: []string{"src/main.cpp"}},
	})
	_, err := runTargetOf(m, "nope")
	if err == nil || !strings.Contains(err.Error(), "Demo") {
		t.Errorf("an unknown target should be answered with the list of real ones: %v", err)
	}
}

// The runtime options are the difference between a sanitizer that fails the
// command and one that prints a report and exits 0. `cppm test` gets them from
// the test preset's environment, which does not apply to a binary cppm launches
// itself, so run has to set them.
func TestSanitizerEnvCarriesTheHaltingOptions(t *testing.T) {
	for _, name := range safety.Sanitizers {
		san, ok := safety.FindSanitizer(name)
		if !ok {
			t.Fatalf("unknown sanitizer %q", name)
		}
		env := sanitizerEnv(san)
		if len(env) != len(san.Env) {
			t.Errorf("--sanitize=%s: %d variables, want %d", name, len(env), len(san.Env))
		}
		joined := strings.Join(env, " ")
		for k, v := range san.Env {
			if !strings.Contains(joined, k+"="+v) {
				t.Errorf("--sanitize=%s: %s=%s missing from %q", name, k, v, joined)
			}
		}
	}
}

// A variable the user exported is appended after cppm's rather than dropped or
// allowed to replace it: the sanitizers read these lists left to right with the
// last setting winning, so cppm's defaults still apply to every option the user
// did not mention.
func TestSanitizerEnvLetsAnExportedValueWin(t *testing.T) {
	t.Setenv("ASAN_OPTIONS", "detect_leaks=0")
	san, _ := safety.FindSanitizer(safety.SanAddress)
	var got string
	for _, kv := range sanitizerEnv(san) {
		if strings.HasPrefix(kv, "ASAN_OPTIONS=") {
			got = kv
		}
	}
	if !strings.HasSuffix(got, ":detect_leaks=0") {
		t.Errorf("the exported value must come last so it wins: %q", got)
	}
	if !strings.Contains(got, "abort_on_error=1") {
		t.Errorf("cppm's own options must still be applied: %q", got)
	}
}

// --error-exitcode=1 is the option that lets a valgrind run fail at all, and
// the direct-run path builds its own argument list rather than going through
// CTest -- so it is a second place the option could have been left out.
func TestValgrindRunArgsKeepTheErrorExitCode(t *testing.T) {
	p := &project.Project{Root: t.TempDir()}
	for _, name := range safety.ValgrindTools {
		tool, _ := safety.FindValgrindTool(name)
		args := valgrindRunArgs(p, tool, false)
		if !reflect.DeepEqual(args, tool.Options) {
			t.Errorf("--%s: args = %q, want the tool's options %q", name, args, tool.Options)
		}
		if tool.FindsErrors && !contains(args, "--error-exitcode=1") {
			t.Errorf("--%s finds errors but cannot fail a run: %q", name, args)
		}
	}
}

func TestGenSuppressionsReachesADirectRun(t *testing.T) {
	p := &project.Project{Root: t.TempDir()}
	tool, _ := safety.FindValgrindTool(safety.ValMemcheck)
	if args := valgrindRunArgs(p, tool, true); !contains(args, safety.GenSuppressionsOption) {
		t.Errorf("--gen-suppressions did not reach valgrind: %q", args)
	}
}

// The flag validation has to behave identically on run and test; the messages
// differ only in the command they tell the user to retype.
func TestRunRejectsTheSameValgrindCombinationsAsTest(t *testing.T) {
	cases := []struct {
		name     string
		choice   valgrindChoice
		sanitize string
		want     string
	}{
		{"sanitizer with valgrind", valgrindChoice{valgrind: safety.ValMemcheck}, safety.SanAddress, "cannot be combined"},
		{"suppressions without valgrind", valgrindChoice{genSupp: true}, "", "only applies to a valgrind run"},
		{"suppressions for a profiler", valgrindChoice{valgrind: safety.ValMassif, genSupp: true}, "", "profiler"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.choice.checkValgrindFlags("run", c.sanitize)
			if err == nil {
				t.Fatalf("expected a refusal")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("message should mention %q: %v", c.want, err)
			}
			// The suggested command must be the one the user typed.
			if strings.Contains(err.Error(), "cppm test") {
				t.Errorf("a run refusal should not suggest a test command: %v", err)
			}
		})
	}
}

// A program's own non-zero exit is not a cppm failure: cppm passes the status
// through and says nothing, so a script sees what it would have seen running
// the binary directly.
func TestPlainExitStatusIsPassedThroughSilently(t *testing.T) {
	err := runResult(toolchain.ExitStatus{Code: 3}, safety.ValgrindTool{}, &runOpts{})
	var ese *exitStatusError
	if !errors.As(err, &ese) {
		t.Fatalf("expected an exitStatusError, got %v", err)
	}
	if ese.code != 3 {
		t.Errorf("exit code = %d, want 3", ese.code)
	}
	if ese.msg != "" {
		t.Errorf("a program's own exit status needs no commentary from cppm: %q", ese.msg)
	}
}

func TestSuccessfulRunReturnsNoError(t *testing.T) {
	if err := runResult(toolchain.ExitStatus{}, safety.ValgrindTool{}, &runOpts{}); err != nil {
		t.Errorf("a clean run must not be an error: %v", err)
	}
}

// A crash under a plain run is the one case where cppm knows the next step, so
// the message carries it -- including the program's own arguments, so the
// suggested command can be retyped as it stands.
func TestACrashSuggestsTheSanitizers(t *testing.T) {
	err := runResult(toolchain.ExitStatus{Code: -1, Signal: "segmentation fault"},
		safety.ValgrindTool{}, &runOpts{args: []string{"--config", "a.toml"}})
	if err == nil {
		t.Fatal("a crash must be reported")
	}
	for _, want := range []string{"segmentation fault", "--sanitize=asan+ubsan", "--memcheck", "-- --config a.toml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the crash message should mention %q: %v", want, err)
		}
	}
}

// A sanitizer finding arrives as SIGABRT, because cppm sets abort_on_error=1.
// Reported as a finding rather than as a crash: "aborted" on its own reads as a
// bug in the program instead of as the sanitizer doing its job.
func TestASanitizerAbortIsReportedAsAFinding(t *testing.T) {
	err := runResult(toolchain.ExitStatus{Code: -1, Signal: "aborted"},
		safety.ValgrindTool{}, &runOpts{sanitize: safety.SanAddress})
	if err == nil {
		t.Fatal("a sanitizer abort must fail the command")
	}
	for _, want := range []string{"asan", "abort_on_error=1", "file and line"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should mention %q: %v", want, err)
		}
	}
}

// Under valgrind the exit status is genuinely ambiguous: --error-exitcode=1
// makes a finding exit 1, and valgrind passes the program's own status through
// otherwise. Saying both is the honest reading.
func TestAValgrindFailureNamesBothPossibilities(t *testing.T) {
	tool, _ := safety.FindValgrindTool(safety.ValMemcheck)
	err := runResult(toolchain.ExitStatus{Code: 1}, tool,
		&runOpts{valgrindChoice: valgrindChoice{valgrind: safety.ValMemcheck}})
	if err == nil {
		t.Fatal("a non-zero status under valgrind must fail the command")
	}
	for _, want := range []string{"memcheck", "reported errors", "program itself failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should mention %q: %v", want, err)
		}
	}
}

// A suggested command has to be the one that fills the tree the user asked
// for: "run 'cppm build'" is wrong advice when the empty tree is build/tsan.
func TestBuildSuggestionMatchesThePreset(t *testing.T) {
	cases := map[string]string{
		"debug":      "cppm build",
		"release":    "cppm build --release",
		"tsan":       "cppm build --sanitize=tsan",
		"asan+ubsan": "cppm build --sanitize=asan+ubsan",
	}
	for preset, want := range cases {
		if got := buildCommandFor(preset); got != want {
			t.Errorf("buildCommandFor(%q) = %q, want %q", preset, got, want)
		}
	}
}

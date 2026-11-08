package cli

import (
	"io"
	"strings"
	"testing"

	"github.com/mehdi-shokohi/cppm/internal/manifest"
)

// defaults returns an initOpts as the flag set would leave it with nothing
// passed, which is the only state askInit is ever called in.
func defaults() *initOpts {
	return &initOpts{std: manifest.DefaultStandard}
}

func ask(t *testing.T, input string, q initQuestions, o *initOpts) string {
	t.Helper()
	var out strings.Builder
	if !askInit(strings.NewReader(input), &out, q, o) {
		t.Fatalf("expected the prompt to be confirmed:\n%s", out.String())
	}
	return out.String()
}

// Pressing Enter through every question must produce exactly what a bare
// `cppm init` produced before the prompt existed. If this drifts, the flag
// defaults and the prompt defaults have come apart and the docs can only be
// right about one of them.
func TestPromptEnterThroughMatchesTheFlagDefaults(t *testing.T) {
	o := defaults()
	ask(t, strings.Repeat("\n", 8), initQuestions{dirArg: "MyApp", suggestedName: "MyApp"}, o)

	if o.name != "MyApp" {
		t.Errorf("name = %q, want MyApp", o.name)
	}
	if o.lib {
		t.Error("kind defaulted to library")
	}
	if o.withTests {
		t.Error("tests defaulted to on for an executable")
	}
	if o.std != manifest.DefaultStandard {
		t.Errorf("std = %d, want %d", o.std, manifest.DefaultStandard)
	}
	if o.compiler != "" {
		t.Errorf("compiler = %q, want empty when none was offered", o.compiler)
	}
}

// A library's test target can link the library, so tests are on by default
// there; an executable's cannot, so they are not. This asymmetry is the whole
// argument for having the prompt choose rather than copying the flag default.
func TestPromptDefaultsTestsOnForALibraryOnly(t *testing.T) {
	lib := defaults()
	ask(t, "\n2\n\n\n\n", initQuestions{suggestedName: "Lib"}, lib)
	if !lib.lib {
		t.Fatal("answering 2 did not select a library")
	}
	if !lib.withTests {
		t.Error("a library should offer tests on the Enter key")
	}

	exe := defaults()
	ask(t, "\n1\n\n\n\n", initQuestions{suggestedName: "App"}, exe)
	if exe.lib || exe.withTests {
		t.Errorf("executable: lib=%v withTests=%v, want false/false", exe.lib, exe.withTests)
	}
}

func TestPromptAcceptsWordsForTheKind(t *testing.T) {
	for _, answer := range []string{"2", "lib", "library", "LIBRARY"} {
		o := defaults()
		ask(t, "\n"+answer+"\n\n\n\n", initQuestions{suggestedName: "L"}, o)
		if !o.lib {
			t.Errorf("kind %q did not select a library", answer)
		}
	}
}

func TestPromptTakesTypedAnswers(t *testing.T) {
	o := defaults()
	out := ask(t, "my app\n2\ny\n17\n", initQuestions{dirArg: "proj", suggestedName: "proj"}, o)

	// A name typed at the prompt goes through the same sanitiser as a
	// directory name, so the prompt cannot produce a manifest that Validate
	// then rejects several steps later.
	if o.name != "my_app" {
		t.Errorf("name = %q, want my_app", o.name)
	}
	if !strings.Contains(out, "my_app") {
		t.Error("the prompt did not report the name it substituted")
	}
	if !o.lib || !o.withTests || o.std != 17 {
		t.Errorf("lib=%v withTests=%v std=%d", o.lib, o.withTests, o.std)
	}
}

func TestPromptRejectsAnInvalidStandardAndAsksAgain(t *testing.T) {
	o := defaults()
	out := ask(t, "App\n1\nn\n21\n17\n", initQuestions{suggestedName: "App"}, o)
	if o.std != 17 {
		t.Errorf("std = %d, want 17 after the retry", o.std)
	}
	if !strings.Contains(out, "21") {
		t.Error("the prompt did not say what was wrong with the answer")
	}
}

func TestPromptRejectsAnUnrecognisedKindAndAsksAgain(t *testing.T) {
	o := defaults()
	out := ask(t, "App\nstatic\n2\n\n\n", initQuestions{suggestedName: "App"}, o)
	if !o.lib {
		t.Error("the retry did not take the second answer")
	}
	if !strings.Contains(out, "not one of the choices") {
		t.Error("an unrecognised kind was accepted silently")
	}
}

func TestPromptPicksACompilerFromTheList(t *testing.T) {
	o := defaults()
	q := initQuestions{suggestedName: "App", compilers: []string{"g++", "clang++"}}
	ask(t, "\n1\nn\n\n2\n", q, o)
	if o.compiler != "clang++" {
		t.Errorf("compiler = %q, want clang++", o.compiler)
	}

	typed := defaults()
	ask(t, "\n1\nn\n\n/opt/llvm/bin/clang++\n", q, typed)
	if typed.compiler != "/opt/llvm/bin/clang++" {
		t.Errorf("compiler = %q, want the typed path", typed.compiler)
	}
}

// Declining at the end must mean nothing happens, and must not be an error:
// changing your mind is a valid outcome, not a failure.
func TestPromptCanBeDeclined(t *testing.T) {
	o := defaults()
	var out strings.Builder
	if askInit(strings.NewReader("App\n1\nn\n\nn\n"), &out, initQuestions{suggestedName: "App"}, o) {
		t.Error("answering n at the confirmation still reported go-ahead")
	}
}

// Closed input must fall through to the defaults rather than loop. The prompt
// is gated on a terminal, but a terminal can be closed mid-question, and a
// question that re-asks on an answer it cannot get is an infinite loop.
func TestPromptOnClosedInputTakesDefaults(t *testing.T) {
	o := defaults()
	var out strings.Builder
	q := initQuestions{suggestedName: "App", compilers: []string{"g++"}}
	if !askInit(strings.NewReader(""), &out, q, o) {
		t.Fatal("EOF should confirm the defaults, not decline")
	}
	if o.name != "App" || o.lib || o.withTests || o.std != manifest.DefaultStandard {
		t.Errorf("EOF did not produce the defaults: %+v", o)
	}
	if o.compiler != "g++" {
		t.Errorf("compiler = %q, want the detected default", o.compiler)
	}
}

// An answer with no trailing newline -- the last line of a here-doc, or a ^D
// straight after typing -- is still an answer.
func TestPromptAcceptsAFinalLineWithoutANewline(t *testing.T) {
	o := defaults()
	ask(t, "App\n2", initQuestions{suggestedName: "App"}, o)
	if !o.lib {
		t.Error("an unterminated final line was discarded")
	}
}

func TestEquivalentInitReproducesTheAnswers(t *testing.T) {
	q := initQuestions{dirArg: "MyApp", suggestedName: "MyApp", compilers: []string{"g++", "clang++"}}
	o := &initOpts{std: 23, lib: true, withTests: true, name: "MyApp", compiler: "clang++"}
	got := equivalentInit(q, o)
	want := "cppm init MyApp --lib --with-tests --std 23 --compiler clang++"
	if got != want {
		t.Errorf("equivalentInit() = %q, want %q", got, want)
	}

	// The detected compiler is left out: the same line pasted on another
	// machine must not pin a compiler the user never asked for.
	o.compiler = "g++"
	o.name = "Other"
	o.lib, o.withTests = false, false
	o.std = manifest.DefaultStandard
	got = equivalentInit(q, o)
	want = "cppm init MyApp --name Other --std 20"
	if got != want {
		t.Errorf("equivalentInit() = %q, want %q", got, want)
	}
}

// The recap exists so the user can learn the flags; if it stops being printed
// the prompt becomes the only way to reach these options.
func TestPromptPrintsTheEquivalentCommand(t *testing.T) {
	o := defaults()
	out := ask(t, "App\n2\ny\n17\n", initQuestions{dirArg: "App", suggestedName: "App"}, o)
	if !strings.Contains(out, "cppm init App --lib --with-tests --std 17") {
		t.Errorf("recap missing the equivalent command:\n%s", out)
	}
}

func TestPromptExplainsWhyItSuggestsADifferentName(t *testing.T) {
	o := defaults()
	q := initQuestions{dirArg: "my-app!", suggestedName: "my-app", nameChanged: true}
	out := ask(t, "\n1\nn\n\n", q, o)
	if !strings.Contains(out, "CMake target name") {
		t.Errorf("no explanation for the substituted name:\n%s", out)
	}
	if o.name != "my-app" {
		t.Errorf("name = %q, want my-app", o.name)
	}
}

var _ io.Writer = (*strings.Builder)(nil)

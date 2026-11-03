package integration_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"nistro-ctl/test/testutils"
)

// The registry drives real behavior scenarios. Help-only invocations do not
// count as coverage: the recorder requires a successful non-help invocation.
type commandScenario struct {
	name     string
	commands []string
	formats  []string
	run      func(*testing.T)
}

var commandScenarios = []commandScenario{
	{"help", []string{"help"}, nil, testHelpCommandBehavior},
	{"authentication", []string{"login", "logout"}, nil, testSavedSessionAcrossProcesses},
	{"deployment reads", []string{
		"inference list", "inference describe", "postgres list", "postgres describe",
	}, []string{"json", "table"}, testDeploymentReadsAndOutput},
	{"discovery JSON", []string{
		"inference offers on-demand", "inference offers scale", "inference recommend-hardware scale",
		"postgres offers on-demand", "postgres offers scale", "postgres placements scale",
	}, []string{"json"}, testDiscoveryRequestsAndJSON},
	{"discovery tables", []string{
		"inference offers on-demand", "inference offers scale", "inference recommend-hardware scale",
		"postgres offers on-demand", "postgres offers scale", "postgres placements scale",
	}, []string{"table"}, testDiscoveryTableOutput},
	{"deployment mutations", []string{
		"inference create on-demand", "inference create scale", "inference update scale", "inference delete",
		"postgres create on-demand", "postgres create scale", "postgres update on-demand", "postgres update scale", "postgres delete",
	}, []string{"json", "table"}, testLocalMutationLifecycle},
}

type commandRun struct {
	args []string
}

type commandRecorder struct {
	sync.Mutex
	runs []commandRun
}

// Scoped by top-level test name, so isolated/parallel regression tests cannot
// make the all-commands scenario pass through an unrelated invocation.
var activeCommandRecorders sync.Map

func commandObserver(t *testing.T) func([]string, testutils.Result) {
	root := strings.SplitN(t.Name(), "/", 2)[0]
	value, ok := activeCommandRecorders.Load(root)
	if !ok {
		return nil
	}
	recorder := value.(*commandRecorder)
	return func(args []string, result testutils.Result) {
		if result.Err != nil || result.ExitCode != 0 || helpRequested(args) {
			return
		}
		recorder.Lock()
		defer recorder.Unlock()
		recorder.runs = append(recorder.runs, commandRun{args: slices.Clone(args)})
	}
}

func helpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" || strings.HasPrefix(arg, "--help=") || strings.HasPrefix(arg, "-h=") {
			return true
		}
	}
	return false
}

func TestAllAvailableCommands(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	cli := newCLI(t, server.URL)
	leaves, groups := discoverCommandTree(t, cli)
	registered := make(map[string]bool)
	for _, scenario := range commandScenarios {
		for _, command := range scenario.commands {
			registered[command] = true
		}
	}
	for command := range leaves {
		if !registered[command] {
			t.Errorf("available command %q has no behavior scenario; add one to commandScenarios", command)
		}
	}
	for command := range registered {
		if !leaves[command] {
			t.Errorf("scenario covers %q, but the binary no longer exposes that command", command)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	// Group commands have navigation behavior rather than an API operation.
	groupsChecked := 0
	for _, group := range groups {
		name := strings.ReplaceAll(group, " ", "/")
		if name == "" {
			name = "root"
		}
		t.Run("navigation/"+name, func(t *testing.T) {
			result := cli.MustRun(t, "", strings.Fields(group)...)
			assertUsagePath(t, result, group)
			groupsChecked++
		})
	}
	if requests.Load() != 0 {
		t.Fatal("command help/navigation contacted the API")
	}

	scenariosChecked := 0
	for _, scenario := range commandScenarios {
		t.Run(scenario.name, func(t *testing.T) {
			recorder := &commandRecorder{}
			root := strings.SplitN(t.Name(), "/", 2)[0]
			activeCommandRecorders.Store(root, recorder)
			t.Cleanup(func() { activeCommandRecorders.Delete(root) })
			// Cleanup also runs after Skip/FailNow. Skipping behavior must not
			// let a registered command pass without an actual execution.
			t.Cleanup(func() { verifyScenarioCoverage(t, scenario, recorder) })
			scenario.run(t)
			scenariosChecked++
		})
	}
	if !t.Failed() && scenariosChecked == len(commandScenarios) && groupsChecked == len(groups) {
		t.Logf("verified behavior for all %d public commands and navigation for %d command groups", len(leaves), len(groups))
	} else if !t.Failed() {
		t.Logf("partial run: %d/%d behavior scenarios and %d/%d groups; run without subtest filters for complete coverage", scenariosChecked, len(commandScenarios), groupsChecked, len(groups))
	}
}

func verifyScenarioCoverage(t *testing.T, scenario commandScenario, recorder *commandRecorder) {
	t.Helper()
	recorder.Lock()
	defer recorder.Unlock()
	formats := scenario.formats
	if len(formats) == 0 {
		formats = []string{""}
	}
	for _, command := range scenario.commands {
		for _, format := range formats {
			found := false
			for _, run := range recorder.runs {
				if invocationMatches(command, run.args) && (format == "" || invocationFormat(run.args) == format) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("scenario never successfully executed %q (output=%q)", command, format)
			}
		}
	}
}

func invocationFormat(args []string) string {
	format := "table"
	for i, arg := range args {
		if (arg == "-o" || arg == "--output") && i+1 < len(args) {
			format = args[i+1]
		} else if strings.HasPrefix(arg, "--output=") {
			format = strings.TrimPrefix(arg, "--output=")
		} else if strings.HasPrefix(arg, "-o=") {
			format = strings.TrimPrefix(arg, "-o=")
		} else if strings.HasPrefix(arg, "-o") && len(arg) > 2 {
			format = arg[2:]
		}
	}
	return format
}

func invocationMatches(command string, args []string) bool {
	// Some authentication scenarios pass --server before the command. All
	// remaining command flags follow the command path in this suite.
	for len(args) > 0 && strings.HasPrefix(args[0], "--server") {
		if args[0] == "--server" && len(args) > 1 {
			args = args[2:]
		} else if strings.HasPrefix(args[0], "--server=") {
			args = args[1:]
		} else {
			return false
		}
	}
	path := strings.Fields(command)
	return len(args) >= len(path) && slices.Equal(args[:len(path)], path)
}

var commandHelpRow = regexp.MustCompile(`^  ([a-zA-Z0-9][a-zA-Z0-9-]*)\s+\S`)

func discoverCommandTree(t *testing.T, cli *testutils.CLI) (map[string]bool, []string) {
	t.Helper()
	leaves := make(map[string]bool)
	var groups []string
	queue := []string{""}
	seen := make(map[string]bool)
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		if seen[path] {
			t.Fatalf("duplicate command path in help: %q", path)
		}
		seen[path] = true
		result := cli.MustRun(t, "", append(strings.Fields(path), "--help")...)
		assertUsagePath(t, result, path)
		var children []string
		inCommands := false
		for _, line := range strings.Split(result.Stdout, "\n") {
			if line == "Available Commands:" {
				inCommands = true
				continue
			}
			if !inCommands {
				continue
			}
			if line == "" {
				inCommands = false
				continue
			}
			match := commandHelpRow.FindStringSubmatch(line)
			if match == nil {
				t.Fatalf("cannot parse command help row for %q", path)
			}
			children = append(children, strings.TrimSpace(path+" "+match[1]))
		}
		if len(children) == 0 {
			leaves[path] = true
		} else {
			groups = append(groups, path)
			queue = append(queue, children...)
		}
	}
	return leaves, groups
}

func assertUsagePath(t *testing.T, result testutils.Result, path string) {
	t.Helper()
	if result.Stderr != "" {
		t.Fatal("help/navigation emitted stderr")
	}
	_, usage, found := strings.Cut(result.Stdout, "Usage:\n")
	if !found {
		t.Fatal("help/navigation did not show Usage")
	}
	line, _, _ := strings.Cut(usage, "\n")
	actual := strings.Fields(line)
	want := append([]string{"nistro"}, strings.Fields(path)...)
	if len(actual) < len(want) || !slices.Equal(actual[:len(want)], want) || len(actual) > len(want) && !strings.HasPrefix(actual[len(want)], "[") && !strings.HasPrefix(actual[len(want)], "<") && actual[len(want)] != "ID" {
		t.Fatalf("help returned a different command path for %q", path)
	}
}

func testHelpCommandBehavior(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("help contacted the API")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	cli := newCLI(t, server.URL)
	for _, path := range []string{"", "inference create on-demand", "postgres update scale"} {
		args := append([]string{"help"}, strings.Fields(path)...)
		assertUsagePath(t, cli.MustRun(t, "", args...), path)
	}
}

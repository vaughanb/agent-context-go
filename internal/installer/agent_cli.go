package installer

import (
	"fmt"
	"os/exec"
	"strings"
)

// cliAgent configures an agent through its own CLI (e.g. `claude mcp add`,
// `codex mcp add`) rather than by editing its config file. This is safer than
// hand-editing formats that may carry comments (TOML) or move between versions.
// The agent is considered present only when its CLI is on PATH; otherwise the
// installer skips it and prints manual instructions.
type cliAgent struct {
	id     string
	name   string
	bin    string
	reload string
	// addArgs / removeArgs build the CLI arguments for the given server.
	addArgs    func(srv Server) []string
	removeArgs func(name string) []string
	// manual returns copy-pasteable instructions for when the CLI is absent.
	manual func(srv Server) string
	// run executes the command; overridable in tests. nil uses exec.
	run func(bin string, args ...string) (string, error)
	// lookPath resolves the CLI on PATH; overridable in tests. nil uses exec.
	lookPath func(string) (string, error)
}

func (a cliAgent) ID() string   { return a.id }
func (a cliAgent) Name() string { return a.name }

func (a cliAgent) Location() string { return a.bin + " CLI" }

func (a cliAgent) Detected() bool {
	lp := a.lookPath
	if lp == nil {
		lp = exec.LookPath
	}
	_, err := lp(a.bin)
	return err == nil
}

func (a cliAgent) exec(bin string, args ...string) (string, error) {
	if a.run != nil {
		return a.run(bin, args...)
	}
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (a cliAgent) Install(srv Server, dryRun bool) Result {
	res := Result{AgentID: a.id, AgentName: a.name, Location: a.Location()}
	if !a.Detected() {
		res.Action = ActionSkipped
		res.Note = a.bin + " CLI not found on PATH" + a.manualSuffix(srv)
		return res
	}
	addArgs := a.addArgs(srv)
	if dryRun {
		res.Action = ActionInstalled
		res.Note = "would run: " + a.bin + " " + strings.Join(addArgs, " ")
		return res
	}
	// Remove first so a re-run replaces any prior definition (idempotent).
	if a.removeArgs != nil {
		_, _ = a.exec(a.bin, a.removeArgs(srv.Name)...)
	}
	if _, err := a.exec(a.bin, addArgs...); err != nil {
		res.Err = err
		return res
	}
	res.Action = ActionInstalled
	res.Note = a.reload
	return res
}

func (a cliAgent) Uninstall(name string, dryRun bool) Result {
	res := Result{AgentID: a.id, AgentName: a.name, Location: a.Location()}
	if !a.Detected() {
		res.Action = ActionSkipped
		res.Note = a.bin + " CLI not found on PATH"
		return res
	}
	if a.removeArgs == nil {
		res.Action = ActionSkipped
		res.Note = "removal not supported for " + a.bin
		return res
	}
	removeArgs := a.removeArgs(name)
	if dryRun {
		res.Action = ActionRemoved
		res.Note = "would run: " + a.bin + " " + strings.Join(removeArgs, " ")
		return res
	}
	if _, err := a.exec(a.bin, removeArgs...); err != nil {
		res.Err = err
		return res
	}
	res.Action = ActionRemoved
	return res
}

func (a cliAgent) manualSuffix(srv Server) string {
	if a.manual == nil {
		return ""
	}
	return "; configure manually:\n" + a.manual(srv)
}

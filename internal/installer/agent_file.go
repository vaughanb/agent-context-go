package installer

import (
	"fmt"
	"path/filepath"
)

// fileAgent configures an agent by read-modify-writing a config file with a
// configWriter. If the existing file cannot be parsed (e.g. it contains
// comments we would drop, or is malformed), it refuses to write and returns a
// skip with manual instructions rather than risk clobbering the user's config.
type fileAgent struct {
	id       string
	name     string
	reload   string
	pathFn   func() (string, bool)
	detectFn func() bool // optional; nil uses the default file/dir heuristic
	writer   configWriter
	manual   func(srv Server, path string) string // instructions when we won't write
}

func (a fileAgent) ID() string   { return a.id }
func (a fileAgent) Name() string { return a.name }

func (a fileAgent) Location() string {
	if p, ok := a.pathFn(); ok {
		return p
	}
	return "(unsupported on this OS)"
}

func (a fileAgent) Detected() bool {
	if a.detectFn != nil {
		return a.detectFn()
	}
	p, ok := a.pathFn()
	if !ok {
		return false
	}
	return pathExists(p) || pathExists(filepath.Dir(p))
}

func (a fileAgent) Install(srv Server, dryRun bool) Result {
	res := Result{AgentID: a.id, AgentName: a.name}
	path, ok := a.pathFn()
	if !ok {
		res.Action = ActionSkipped
		res.Note = "not supported on this operating system"
		return res
	}
	res.Location = path

	existing, err := readFileOrEmpty(path)
	if err != nil {
		res.Err = err
		return res
	}
	newData, err := a.writer.upsert(existing, srv)
	if err != nil {
		// Do not overwrite a config we could not parse.
		res.Action = ActionSkipped
		res.Note = a.manualNote(srv, path, err)
		return res
	}

	if sameContent(existing, newData) {
		res.Action = ActionUnchanged
		res.Note = "already configured"
		return res
	}

	res.Action = ActionInstalled
	if _, had, _ := a.writer.remove(existing, srv.Name); had {
		res.Action = ActionUpdated
	}
	if dryRun {
		res.Note = "would write " + string(res.Action)
		return res
	}

	backup, err := writeConfig(path, existing, newData)
	if err != nil {
		res.Err = err
		return res
	}
	res.Backup = backup
	res.Note = a.reload
	return res
}

func (a fileAgent) Uninstall(name string, dryRun bool) Result {
	res := Result{AgentID: a.id, AgentName: a.name}
	path, ok := a.pathFn()
	if !ok {
		res.Action = ActionSkipped
		res.Note = "not supported on this operating system"
		return res
	}
	res.Location = path

	existing, err := readFileOrEmpty(path)
	if err != nil {
		res.Err = err
		return res
	}
	if len(existing) == 0 {
		res.Action = ActionUnchanged
		res.Note = "no config file"
		return res
	}
	newData, found, err := a.writer.remove(existing, name)
	if err != nil {
		res.Action = ActionSkipped
		res.Note = fmt.Sprintf("left unchanged (could not parse config: %v)", err)
		return res
	}
	if !found {
		res.Action = ActionUnchanged
		res.Note = "not present"
		return res
	}
	if dryRun {
		res.Action = ActionRemoved
		res.Note = "would remove"
		return res
	}
	backup, err := writeConfig(path, existing, newData)
	if err != nil {
		res.Err = err
		return res
	}
	res.Action = ActionRemoved
	res.Backup = backup
	return res
}

func (a fileAgent) manualNote(srv Server, path string, parseErr error) string {
	if a.manual != nil {
		return fmt.Sprintf("existing config left untouched (%v); add manually:\n%s", parseErr, a.manual(srv, path))
	}
	return fmt.Sprintf("existing config left untouched (%v); please add the server manually", parseErr)
}

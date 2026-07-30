package indexer

import "errors"

// ErrAlreadyRunning is returned by Index when an index run is already in
// progress on the same Indexer.
var ErrAlreadyRunning = errors.New("indexer: an index run is already in progress")

// Phase names the current stage of an index run.
type Phase string

const (
	// PhaseIdle is the state before any run has started.
	PhaseIdle Phase = "idle"
	// PhaseWalking is directory traversal and file selection.
	PhaseWalking Phase = "walking"
	// PhaseIndexing is chunking and embedding changed files.
	PhaseIndexing Phase = "indexing"
	// PhasePruning is deleting index entries for files gone from disk.
	PhasePruning Phase = "pruning"
	// PhaseDone marks a completed run.
	PhaseDone Phase = "done"
)

// Progress is a snapshot of an index run. It is returned by value so callers
// never touch the Indexer's live state.
type Progress struct {
	Phase Phase
	// Total is the number of candidate files; valid once Phase reaches
	// PhaseIndexing.
	Total int
	// Done is the number of files processed so far.
	Done int
	// Errors holds per-file (and fatal) error messages encountered this run.
	Errors []string
}

// Status returns a snapshot of the current or most recent run. Before any run
// it reports PhaseIdle.
func (ix *Indexer) Status() Progress {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	p := ix.progress
	if p.Phase == "" {
		p.Phase = PhaseIdle
	}
	// Copy the slice so the caller cannot observe later mutation.
	if len(p.Errors) > 0 {
		p.Errors = append([]string(nil), p.Errors...)
	}
	return p
}

// Running reports whether an index run is currently in progress.
func (ix *Indexer) Running() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.running
}

func (ix *Indexer) setPhase(p Phase) {
	ix.mu.Lock()
	ix.progress.Phase = p
	ix.mu.Unlock()
}

func (ix *Indexer) setTotal(n int) {
	ix.mu.Lock()
	ix.progress.Total = n
	ix.mu.Unlock()
}

func (ix *Indexer) incDone() {
	ix.mu.Lock()
	ix.progress.Done++
	ix.mu.Unlock()
}

func (ix *Indexer) addError(msg string) {
	ix.mu.Lock()
	ix.progress.Errors = append(ix.progress.Errors, msg)
	ix.mu.Unlock()
}

// fail records a fatal error and moves the run to PhaseDone.
func (ix *Indexer) fail(msg string) {
	ix.mu.Lock()
	ix.progress.Errors = append(ix.progress.Errors, msg)
	ix.progress.Phase = PhaseDone
	ix.mu.Unlock()
}

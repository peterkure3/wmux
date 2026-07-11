package daemon

import (
	"fmt"
	"sync"
	"time"

	"github.com/peterkure/wmux/internal/proto"
)

// paneSpecTTL is how long a filed pane spec stays claimable. A spec that
// was never claimed (wt.exe failed to open, the "wmux" profile wasn't
// loaded yet, ...) shouldn't linger forever — a much later, unrelated
// `wt --profile wmux` launch would otherwise claim it and unexpectedly
// start an agent.
const paneSpecTTL = 2 * time.Minute

type pendingPane struct {
	spec  proto.PaneSpec
	filed time.Time
}

// paneSpecs holds specs filed by `wmux pane` that no pane has claimed yet.
// Deliberately in-memory only (not persisted): a spec is a handshake with a
// wt.exe launch that happens within seconds, not durable session state.
type paneSpecs struct {
	mu      sync.Mutex
	pending map[string]pendingPane
}

// AddPaneSpec files (or replaces) the pending spec for a session ID.
func (d *Daemon) AddPaneSpec(spec proto.PaneSpec) {
	d.panes.mu.Lock()
	defer d.panes.mu.Unlock()
	if d.panes.pending == nil {
		d.panes.pending = make(map[string]pendingPane)
	}
	d.panes.pending[spec.ID] = pendingPane{spec: spec, filed: time.Now()}
}

// ClaimPaneSpec hands the pending spec for id to the pane that will run
// it, removing it so a spec can only ever start one agent.
func (d *Daemon) ClaimPaneSpec(id string) (proto.PaneSpec, error) {
	d.panes.mu.Lock()
	defer d.panes.mu.Unlock()
	p, ok := d.panes.pending[id]
	if !ok {
		return proto.PaneSpec{}, fmt.Errorf("no pending pane spec for session %q", id)
	}
	delete(d.panes.pending, id)
	if time.Since(p.filed) > paneSpecTTL {
		return proto.PaneSpec{}, fmt.Errorf("pane spec for session %q expired (filed %s ago)", id, time.Since(p.filed).Round(time.Second))
	}
	return p.spec, nil
}

package opcuaserver

import (
	"context"
	"fmt"
	"time"

	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
)

// saveDebounce is how long a save waits after a write, so that a client
// updating many tags in one burst produces one save rather than one per tag.
const saveDebounce = 250 * time.Millisecond

// Persister keeps the tag values on disk so that they survive a restart.
//
// Values are read back out of the address space rather than intercepted on the
// way in, because gopcua's write path replaces a node's value function outright
// and offers no hook. The namespace does expose an ExternalNotification channel
// that fires on every write, which is used to save promptly; the periodic sweep
// is what makes the result reliable, since that channel drops notifications
// when nobody is listening at that instant.
type Persister struct {
	path     string
	ns       *server.NodeNameSpace
	nodeIDs  []string
	types    map[string]DataType
	logger   *Logger
	interval time.Duration

	last map[string]any
}

// NewPersister prepares to persist the given tags to path.
func NewPersister(path string, ns *server.NodeNameSpace, tags []Tag, f NodeIDFormat, interval time.Duration, logger *Logger) *Persister {
	p := &Persister{
		path:     path,
		ns:       ns,
		nodeIDs:  make([]string, 0, len(tags)),
		types:    make(map[string]DataType, len(tags)),
		logger:   logger,
		interval: interval,
	}
	for _, t := range tags {
		id := t.NodeID(f)
		p.nodeIDs = append(p.nodeIDs, id)
		p.types[id] = t.DataType
	}
	return p
}

// MarkSaved records the values the server started with, so that the first sweep
// does not rewrite a file that nothing has changed.
func (p *Persister) MarkSaved() {
	p.last = p.snapshot()
}

// snapshot reads the current value of every tag.
func (p *Persister) snapshot() map[string]any {
	out := make(map[string]any, len(p.nodeIDs))
	nsID := p.ns.ID()
	for _, id := range p.nodeIDs {
		dv := p.ns.Attribute(ua.NewStringNodeID(nsID, id), ua.AttributeIDValue)
		if dv == nil || dv.Value == nil {
			continue
		}
		out[id] = dv.Value.Value()
	}
	return out
}

// SaveIfChanged writes the value file when any tag differs from what was last
// written, and reports how many did.
func (p *Persister) SaveIfChanged() (int, error) {
	now := p.snapshot()

	changed := 0
	for id, v := range now {
		if prev, ok := p.last[id]; !ok || prev != v {
			changed++
		}
	}
	if changed == 0 {
		return 0, nil
	}

	if err := SaveValues(p.path, now, p.types); err != nil {
		return changed, err
	}
	p.last = now
	return changed, nil
}

// Run saves on every change until ctx is cancelled, then saves once more so
// that a value written just before shutdown is not lost. It returns when that
// final save is done.
func (p *Persister) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	save := func(reason string) {
		n, err := p.SaveIfChanged()
		switch {
		case err != nil:
			p.logger.Error("could not save tag values to %s: %s", p.path, err)
		case n > 0:
			p.logger.Info("saved %d changed tag value(s) to %s (%s)", n, p.path, reason)
		}
	}

	for {
		select {
		case <-ctx.Done():
			save("shutdown")
			return

		case <-ticker.C:
			save("periodic")

		case <-p.ns.ExternalNotification:
			// Let a burst of writes settle into a single save.
			p.drain(ctx)
			save("write")
		}
	}
}

// drain waits out the debounce window, swallowing further notifications.
func (p *Persister) drain(ctx context.Context) {
	timer := time.NewTimer(saveDebounce)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-p.ns.ExternalNotification:
		}
	}
}

// RestoreValues turns a loaded value file into initial values for Build. Values
// whose tag has disappeared from the tag file, or whose data type no longer
// matches, are reported and left out rather than forced back in.
func RestoreValues(stored map[string]storedValue, tags []Tag, f NodeIDFormat) (map[string]any, []string) {
	if len(stored) == 0 {
		return nil, nil
	}

	want := make(map[string]DataType, len(tags))
	for _, t := range tags {
		want[t.NodeID(f)] = t.DataType
	}

	out := make(map[string]any, len(stored))
	var warnings []string
	unknown := 0

	for id, sv := range stored {
		dt, ok := want[id]
		if !ok {
			unknown++
			continue
		}
		if sv.Type != "" && sv.Type != dt {
			warnings = append(warnings, fmt.Sprintf("tag %q is %s in the tag file but was saved as %s, keeping the default", id, dt, sv.Type))
			continue
		}
		v, err := decodeValue(dt, sv.Value)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("tag %q has an unreadable saved value, keeping the default: %v", id, err))
			continue
		}
		out[id] = v
	}

	if unknown > 0 {
		warnings = append(warnings, fmt.Sprintf("%d saved value(s) are for tags that are no longer in the tag file", unknown))
	}
	return out, warnings
}

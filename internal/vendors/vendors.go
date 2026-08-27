package vendors

import (
	"maps"
	"slices"
)

// Options carries the overrides a vendor runner block may set: Model and
// Effort override the adapter's own defaults, and Args is the escape valve
// for a flag the adapter doesn't model, spliced in verbatim.
type Options struct {
	Model  string
	Effort string
	Args   string
}

// Adapter turns Options into the command and resume templates a vendor
// runner resolves to — the same shape a hand-written config.Runner fills,
// placeholders and all, so everything downstream needs no case for vendors.
type Adapter interface {
	Command(Options) string
	Resume(Options) string
}

// adapters is the whole set of vendors lerp ships. Adding one is one file
// plus one entry here — logfmt's detect carries the same comment on the
// decoding side. A map rather than a switch is what keeps Names from
// drifting away from what Lookup accepts.
var adapters = map[string]Adapter{
	"claude": claude{},
}

// Lookup returns the adapter registered under name.
func Lookup(name string) (Adapter, bool) {
	a, ok := adapters[name]
	return a, ok
}

// Names lists the known vendor names, sorted, for an error message that
// names what config could have said instead.
func Names() []string {
	return slices.Sorted(maps.Keys(adapters))
}

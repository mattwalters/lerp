package vendors

import "testing"

func TestLookupKnownAndUnknown(t *testing.T) {
	if _, ok := Lookup("claude"); !ok {
		t.Error(`Lookup("claude") = false, want true`)
	}
	if _, ok := Lookup("nonesuch"); ok {
		t.Error(`Lookup("nonesuch") = true, want false`)
	}
}

// Names is what an "unknown vendor" error lists as the alternative; it must
// never claim a name Lookup then refuses.
func TestNamesAgreesWithLookup(t *testing.T) {
	names := Names()
	if len(names) != len(adapters) {
		t.Errorf("Names() = %v, want one entry per adapter (%d)", names, len(adapters))
	}
	for _, name := range names {
		if _, ok := Lookup(name); !ok {
			t.Errorf("Names() lists %q but Lookup does not recognize it", name)
		}
	}
}

// Every vendor must supply a non-empty BypassArgs spelling for unattended runs.
func TestBypassArgsNonEmpty(t *testing.T) {
	for _, name := range Names() {
		adapter, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) = false", name)
		}
		if got := adapter.BypassArgs(); got == "" {
			t.Errorf("adapter %q returned empty BypassArgs()", name)
		}
	}
}

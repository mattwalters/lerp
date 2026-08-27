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

package logfmt

// raw is the fallback and the floor: every line is text, exactly as the
// runner wrote it. It is what a runner nobody has written a decoder for gets,
// and it is why writing one is optional.
type raw struct{}

func (raw) Decode(line string) (Event, bool) {
	return Event{Kind: KindText, Text: line}, true
}

package sentinellogrus

import "testing"

func TestInstrumentRejectsNilLogger(t *testing.T) {
	if err := Instrument(nil); err == nil {
		t.Fatal("Instrument(nil) error = nil")
	}
}

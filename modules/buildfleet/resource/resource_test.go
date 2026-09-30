package resource

import "testing"

func TestParseQuantity(t *testing.T) {
	tests := []struct {
		in      string
		want    Bytes
		wantErr bool
	}{
		{"512Mi", 512 * 1024 * 1024, false},
		{"4Gi", 4 * 1024 * 1024 * 1024, false},
		{"1.5Gi", Bytes(1.5 * 1024 * 1024 * 1024), false},
		{"1024", 1024, false},
		{"Gi", 0, true},
		{"1Xi", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseQuantity(tt.in)
		if (err != nil) != tt.wantErr {
			t.Fatalf("%q: err=%v wantErr=%v", tt.in, err, tt.wantErr)
		}
		if err == nil && got != tt.want {
			t.Fatalf("%q: got=%d want=%d", tt.in, got, tt.want)
		}
	}
}

func TestFits(t *testing.T) {
	small := Resources{CPU: 1, Memory: 2 * Giga}
	if !(small).Fits(Resources{CPU: 1, Memory: 2 * Giga}) {
		t.Fatal("equal should fit")
	}
	if small.Fits(Resources{CPU: 2}) {
		t.Fatal("cpu overflow should not fit")
	}
	if small.Fits(Resources{Memory: 3 * Giga}) {
		t.Fatal("mem overflow should not fit")
	}
}

func TestSpecTag(t *testing.T) {
	tests := []struct {
		in   Resources
		want string
	}{
		{Resources{CPU: 1, Memory: 2 * Giga}, "1c2g"},
		{Resources{CPU: 2, Memory: 4 * Giga}, "2c4g"},
		{Resources{CPU: 4, Memory: 8 * Giga}, "4c8g"},
		{Resources{CPU: 0.5, Memory: 1 * Giga}, "500mc1g"},
		{Resources{}, "0c0g"},
		{Resources{CPU: 1, Memory: 512 * Mega}, "1c512m"},
	}
	for _, tt := range tests {
		if got := tt.in.SpecTag(); got != tt.want {
			t.Fatalf("%+v: got %q want %q", tt.in, got, tt.want)
		}
	}
}

func TestFitsEphemeralStorage(t *testing.T) {
	cap := Resources{CPU: 8, Memory: 16 * Giga} // ephemeral undeclared
	if !cap.Fits(Resources{CPU: 2, Memory: 4 * Giga, EphemeralStorage: 50 * Giga}) {
		t.Fatal("undeclared ephemeral capacity must be unbounded")
	}
	cap.EphemeralStorage = 20 * Giga
	if cap.Fits(Resources{CPU: 2, Memory: 4 * Giga, EphemeralStorage: 30 * Giga}) {
		t.Fatal("30Gi request must not fit 20Gi capacity")
	}
	if !cap.Fits(Resources{CPU: 2, Memory: 4 * Giga, EphemeralStorage: 20 * Giga}) {
		t.Fatal("exact fit should pass")
	}
}

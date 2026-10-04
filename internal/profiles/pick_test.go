package profiles

import "testing"

func TestPick(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		available []string
		onAC      bool
		want      string
	}{
		{"no ppd means nothing to do", nil, true, ""},
		{"performance on AC when available", []string{"performance", "balanced"}, true, "performance"},
		{"balanced on AC without performance", []string{"power-saver", "balanced"}, true, "balanced"},
		{"power-saver on battery when available", []string{"power-saver", "balanced"}, false, "power-saver"},
		{"balanced on battery without power-saver", []string{"performance", "balanced"}, false, "balanced"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Pick(tt.available, tt.onAC); got != tt.want {
				t.Errorf("Pick(%v, %v) = %q, want %q", tt.available, tt.onAC, got, tt.want)
			}
		})
	}
}

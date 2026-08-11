package app

import "testing"

func TestContextWindowForModel(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{model: "deepseek-v4-flash", want: 1_000_000},
		{model: "deepseek-v4-pro", want: 1_000_000},
		{model: "MiniMax-M3", want: 1_000_000},
		{model: "MiniMax-M2.7", want: 204_800},
		{model: "deepseek-chat", want: 128_000},
		{model: "unknown-model", want: 128_000},
		{model: "", want: 128_000},
	}
	for _, tt := range tests {
		name := tt.model
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			if got := contextWindowForModel(tt.model); got != tt.want {
				t.Fatalf("contextWindowForModel(%q) = %d, want %d", tt.model, got, tt.want)
			}
		})
	}
}

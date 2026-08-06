package defaults

import "testing"

func TestContextWindowForModel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model string
		want  int
	}{
		{"v4 flash", "deepseek-v4-flash", DeepSeekV4ContextWindow},
		{"v4 pro", "deepseek-v4-pro", DeepSeekV4ContextWindow},
		{"v4 flash mixed case", "DeepSeek-V4-Flash", DeepSeekV4ContextWindow},
		{"v4 pro padded", "  deepseek-v4-pro  ", DeepSeekV4ContextWindow},
		{"retired chat alias", "deepseek-chat", DefaultContextWindow},
		{"retired reasoner", "deepseek-reasoner", DefaultContextWindow},
		{"unknown model", "some-unknown-model", DefaultContextWindow},
		{"empty", "", DefaultContextWindow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContextWindowForModel(tc.model); got != tc.want {
				t.Fatalf("ContextWindowForModel(%q) = %d, want %d", tc.model, got, tc.want)
			}
		})
	}
}

func TestIsSupportedModel(t *testing.T) {
	for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-pro"} {
		if !IsSupportedModel(model) {
			t.Fatalf("expected %q to be supported", model)
		}
	}
	// The ACP entrypoint historically hardcoded the retired "deepseek-chat"
	// alias; it must NOT validate — that is how the alias is deleted.
	for _, model := range []string{"deepseek-chat", "deepseek-reasoner", "", "gpt-4o"} {
		if IsSupportedModel(model) {
			t.Fatalf("expected %q to be unsupported", model)
		}
	}
}

func TestSupportedModelsIncludesDefaultModel(t *testing.T) {
	for _, m := range SupportedModels() {
		if m == DefaultModel {
			return
		}
	}
	t.Fatalf("SupportedModels() = %v does not include DefaultModel %q", SupportedModels(), DefaultModel)
}

func TestDefaultMaxToolItersStaysFinite(t *testing.T) {
	// The ACP cap must stay a finite backstop: the dynamic loop guards cannot
	// see mutating-argument-varying loops, so removing the cap would re-open
	// that loop class (see agent tests). 300 sits above every recorded healthy
	// turn (max 126) while bounding the invisible loops.
	if DefaultMaxToolIters < 200 || DefaultMaxToolIters > 500 {
		t.Fatalf("DefaultMaxToolIters = %d, want a finite backstop in [200,500]", DefaultMaxToolIters)
	}
}

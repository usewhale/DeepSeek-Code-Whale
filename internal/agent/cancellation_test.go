package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestUserInterruptCarriesSource(t *testing.T) {
	err := NewUserInterrupt(" ctrl+c ")
	if !errors.Is(err, ErrUserInterrupt) {
		t.Fatalf("expected user interrupt sentinel, got %v", err)
	}
	if got := UserInterruptSource(err); got != "ctrl+c" {
		t.Fatalf("source = %q, want ctrl+c", got)
	}
	if got := UserInterruptSource(ErrUserInterrupt); got != "" {
		t.Fatalf("sentinel source = %q, want empty", got)
	}
}

func TestCancellationErrorDetailIncludesContextCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(NewUserInterrupt("esc"))
	err := fmt.Errorf("request failed: %w", context.Canceled)

	got := cancellationErrorDetail(ctx, err)
	for _, want := range []string{"request failed: context canceled", "cancel cause: user interrupt (source: esc)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("detail %q does not contain %q", got, want)
		}
	}
}

func TestCancellationErrorDetailLeavesPlainCancelStable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := cancellationErrorDetail(ctx, context.Canceled); got != "context canceled" {
		t.Fatalf("detail = %q, want context canceled", got)
	}
}

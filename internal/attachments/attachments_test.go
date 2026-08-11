package attachments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usewhale/whale/internal/core"
)

func TestPrepareCopiesAttachmentIntoSessionDir(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "note.txt")
	data := []byte("hello attachment")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	parts, prepared, err := PrepareMessageParts(context.Background(), "inspect", []Source{{Path: src}}, Options{
		SessionsDir: filepath.Join(tmp, "sessions"),
		SessionID:   "session-1",
	})
	if err != nil {
		t.Fatalf("PrepareMessageParts: %v", err)
	}
	if len(parts) != 2 || parts[0].Type != core.MessagePartText || parts[1].Type != core.MessagePartAttachment {
		t.Fatalf("parts = %+v", parts)
	}
	if len(prepared) != 1 {
		t.Fatalf("prepared len = %d", len(prepared))
	}
	ref := prepared[0].Ref
	if ref.Kind != core.AttachmentKindFile || !strings.HasPrefix(ref.MIME, "text/plain") {
		t.Fatalf("kind/mime = %s/%s", ref.Kind, ref.MIME)
	}
	if ref.Path == src || !strings.Contains(ref.Path, "session-1.attachments") {
		t.Fatalf("stored path = %q", ref.Path)
	}
	if ref.OriginalPath != src {
		t.Fatalf("original path = %q, want %q", ref.OriginalPath, src)
	}
	got, err := os.ReadFile(ref.Path)
	if err != nil {
		t.Fatalf("read stored: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("stored content = %q", got)
	}
	sum := sha256.Sum256(data)
	if ref.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha = %q", ref.SHA256)
	}
	if gotText := core.MessagePartsPlainText(parts); !strings.Contains(gotText, "inspect") || !strings.Contains(gotText, "[file: note.txt]") {
		t.Fatalf("plain text = %q", gotText)
	}
}

func TestPrepareClassifiesPDFAndRejectsBadPDF(t *testing.T) {
	tmp := t.TempDir()
	good := filepath.Join(tmp, "doc.pdf")
	if err := os.WriteFile(good, []byte("%PDF-1.7\nbody"), 0o644); err != nil {
		t.Fatalf("write good pdf: %v", err)
	}
	prepared, err := Prepare(context.Background(), []Source{{Path: good}}, Options{
		SessionsDir: filepath.Join(tmp, "sessions"),
		SessionID:   "s",
	})
	if err != nil {
		t.Fatalf("Prepare good pdf: %v", err)
	}
	if prepared[0].Ref.Kind != core.AttachmentKindPDF || prepared[0].Ref.MIME != "application/pdf" {
		t.Fatalf("pdf ref = %+v", prepared[0].Ref)
	}

	bad := filepath.Join(tmp, "bad.pdf")
	if err := os.WriteFile(bad, []byte("<html>not pdf</html>"), 0o644); err != nil {
		t.Fatalf("write bad pdf: %v", err)
	}
	if _, err := Prepare(context.Background(), []Source{{Path: bad}}, Options{
		SessionsDir: filepath.Join(tmp, "sessions"),
		SessionID:   "s2",
	}); err == nil || !strings.Contains(err.Error(), "valid PDF") {
		t.Fatalf("expected bad pdf error, got %v", err)
	}
}

func TestPrepareAudioFormatsMatchOpenAICompatibleEncoder(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"sound.mp3", "sound.wav"} {
		t.Run("accept "+name, func(t *testing.T) {
			path := filepath.Join(tmp, name)
			if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
				t.Fatalf("write audio: %v", err)
			}
			prepared, err := Prepare(context.Background(), []Source{{Path: path}}, Options{
				SessionsDir: filepath.Join(tmp, "sessions"),
				SessionID:   strings.TrimSuffix(name, filepath.Ext(name)),
			})
			if err != nil {
				t.Fatalf("Prepare %s: %v", name, err)
			}
			if prepared[0].Ref.Kind != core.AttachmentKindAudio {
				t.Fatalf("kind = %s, want audio", prepared[0].Ref.Kind)
			}
		})
	}

	for _, name := range []string{"sound.m4a", "sound.ogg", "sound.flac", "sound.webm"} {
		t.Run("reject "+name, func(t *testing.T) {
			path := filepath.Join(tmp, name)
			if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
				t.Fatalf("write audio: %v", err)
			}
			_, err := Prepare(context.Background(), []Source{{Path: path}}, Options{
				SessionsDir: filepath.Join(tmp, "sessions"),
				SessionID:   strings.TrimSuffix(name, filepath.Ext(name)),
			})
			if err == nil || !strings.Contains(err.Error(), "supported audio formats are mp3 and wav") {
				t.Fatalf("error = %v, want unsupported audio format error", err)
			}
		})
	}
}

func TestPrepareClassifiesVideo(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "clip.mp4")
	if err := os.WriteFile(src, []byte("fake-video"), 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	prepared, err := Prepare(context.Background(), []Source{{Path: src}}, Options{
		SessionsDir:   filepath.Join(tmp, "sessions"),
		SessionID:     "video",
		MaxVideoBytes: 1024,
	})
	if err != nil {
		t.Fatalf("Prepare video: %v", err)
	}
	if prepared[0].Ref.Kind != core.AttachmentKindVideo || prepared[0].Ref.MIME != "video/mp4" {
		t.Fatalf("kind/mime = %s/%s", prepared[0].Ref.Kind, prepared[0].Ref.MIME)
	}
}

func TestPrepareFollowsSymlinkToRegularFile(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "target.png")
	if err := os.WriteFile(target, append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("payload")...), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(tmp, "link.png")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	prepared, err := Prepare(context.Background(), []Source{{Path: link}}, Options{
		SessionsDir: filepath.Join(tmp, "sessions"),
		SessionID:   "s",
	})
	if err != nil {
		t.Fatalf("Prepare symlink: %v", err)
	}
	if prepared[0].Ref.Kind != core.AttachmentKindImage {
		t.Fatalf("kind = %s", prepared[0].Ref.Kind)
	}
	if prepared[0].Ref.OriginalPath != link {
		t.Fatalf("original = %q, want %q", prepared[0].Ref.OriginalPath, link)
	}
}

func TestPrepareRejectsInvalidInputs(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "big.txt")
	if err := os.WriteFile(file, []byte("too big"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	cases := []struct {
		name string
		src  Source
		opts Options
		want string
	}{
		{
			name: "bad session",
			src:  Source{Path: file},
			opts: Options{SessionsDir: filepath.Join(tmp, "sessions"), SessionID: "../bad"},
			want: "invalid session",
		},
		{
			name: "too large",
			src:  Source{Path: file},
			opts: Options{SessionsDir: filepath.Join(tmp, "sessions"), SessionID: "s", MaxFileBytes: 3},
			want: "exceeds size limit",
		},
		{
			name: "directory",
			src:  Source{Path: tmp},
			opts: Options{SessionsDir: filepath.Join(tmp, "sessions"), SessionID: "s"},
			want: "directory",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Prepare(context.Background(), []Source{tc.src}, tc.opts); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want contains %q", err, tc.want)
			}
		})
	}
}

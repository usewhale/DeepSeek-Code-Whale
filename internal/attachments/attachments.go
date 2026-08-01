package attachments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/securefs"
)

const (
	DefaultMaxAttachments = 10
	DefaultMaxTotalBytes  = 50 * 1024 * 1024

	DefaultMaxImageBytes = 10 * 1024 * 1024
	DefaultMaxPDFBytes   = 20 * 1024 * 1024
	DefaultMaxAudioBytes = 25 * 1024 * 1024
	DefaultMaxVideoBytes = 50 * 1024 * 1024
	DefaultMaxFileBytes  = 20 * 1024 * 1024
)

type Source struct {
	Path        string
	DisplayName string
}

type Options struct {
	SessionsDir    string
	SessionID      string
	WorkspaceRoot  string
	MaxAttachments int
	MaxTotalBytes  int64
	MaxImageBytes  int64
	MaxPDFBytes    int64
	MaxAudioBytes  int64
	MaxVideoBytes  int64
	MaxFileBytes   int64
}

type Prepared struct {
	Ref core.AttachmentRef
}

func PrepareMessageParts(ctx context.Context, text string, sources []Source, opts Options) ([]core.MessagePart, []Prepared, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	parts := make([]core.MessagePart, 0, 1+len(sources))
	if text != "" {
		parts = append(parts, core.MessagePart{Type: core.MessagePartText, Text: text})
	}
	prepared, err := Prepare(ctx, sources, opts)
	if err != nil {
		return nil, nil, err
	}
	for i := range prepared {
		ref := prepared[i].Ref
		parts = append(parts, core.MessagePart{Type: core.MessagePartAttachment, Attachment: &ref})
	}
	return parts, prepared, nil
}

func Prepare(ctx context.Context, sources []Source, opts Options) ([]Prepared, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits := normalizeOptions(opts)
	if len(sources) > limits.MaxAttachments {
		return nil, fmt.Errorf("too many attachments: got %d, max %d", len(sources), limits.MaxAttachments)
	}
	if strings.TrimSpace(limits.SessionsDir) == "" {
		return nil, fmt.Errorf("sessions dir is required")
	}
	if !validSessionID(limits.SessionID) {
		return nil, fmt.Errorf("invalid session id for attachments")
	}
	attachmentsDir := filepath.Join(limits.SessionsDir, limits.SessionID+".attachments")
	if err := securefs.MkdirPrivate(attachmentsDir); err != nil {
		return nil, fmt.Errorf("prepare attachments dir: %w", err)
	}

	out := make([]Prepared, 0, len(sources))
	var total int64
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ref, err := prepareOne(src, attachmentsDir, limits)
		if err != nil {
			return nil, err
		}
		total += ref.SizeBytes
		if total > limits.MaxTotalBytes {
			return nil, fmt.Errorf("attachments exceed total size limit: got %d bytes, max %d bytes", total, limits.MaxTotalBytes)
		}
		out = append(out, Prepared{Ref: ref})
	}
	return out, nil
}

func prepareOne(src Source, attachmentsDir string, opts Options) (core.AttachmentRef, error) {
	rawPath := strings.TrimSpace(src.Path)
	if rawPath == "" {
		return core.AttachmentRef{}, fmt.Errorf("attachment path is required")
	}
	abs, err := filepath.Abs(rawPath)
	if err != nil {
		return core.AttachmentRef{}, fmt.Errorf("resolve attachment path %q: %w", rawPath, err)
	}
	abs = filepath.Clean(abs)
	resolved := abs
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		resolved = filepath.Clean(real)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return core.AttachmentRef{}, fmt.Errorf("stat attachment %q: %w", rawPath, err)
	}
	if info.IsDir() {
		return core.AttachmentRef{}, fmt.Errorf("attachment is a directory: %s", rawPath)
	}
	if !info.Mode().IsRegular() {
		return core.AttachmentRef{}, fmt.Errorf("attachment is not a regular file: %s", rawPath)
	}
	if info.Size() == 0 {
		return core.AttachmentRef{}, fmt.Errorf("attachment is empty: %s", rawPath)
	}

	file, err := os.Open(resolved)
	if err != nil {
		return core.AttachmentRef{}, fmt.Errorf("open attachment %q: %w", rawPath, err)
	}
	defer file.Close()

	header := make([]byte, 512)
	n, err := io.ReadFull(file, header)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return core.AttachmentRef{}, fmt.Errorf("read attachment header %q: %w", rawPath, err)
	}
	header = header[:n]
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return core.AttachmentRef{}, fmt.Errorf("seek attachment %q: %w", rawPath, err)
	}

	kind, mime := classifyAttachment(resolved, header)
	if kind == core.AttachmentKindPDF && !bytesHasPDFHeader(header) {
		return core.AttachmentRef{}, fmt.Errorf("attachment is not a valid PDF: %s", rawPath)
	}
	if kind == core.AttachmentKindAudio && !supportedAudioAttachment(mime, resolved) {
		return core.AttachmentRef{}, fmt.Errorf("audio attachment uses unsupported format: %s; supported audio formats are mp3 and wav", rawPath)
	}
	maxBytes := maxBytesForKind(kind, opts)
	if info.Size() > maxBytes {
		return core.AttachmentRef{}, fmt.Errorf("%s attachment exceeds size limit: got %d bytes, max %d bytes", kind, info.Size(), maxBytes)
	}

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return core.AttachmentRef{}, fmt.Errorf("hash attachment %q: %w", rawPath, err)
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	name := sanitizeFilename(filepath.Base(resolved))
	storedPath := filepath.Join(attachmentsDir, sum+"-"+name)
	if _, err := os.Stat(storedPath); err != nil {
		if !os.IsNotExist(err) {
			return core.AttachmentRef{}, fmt.Errorf("stat stored attachment: %w", err)
		}
		if err := copyPrivateFile(resolved, storedPath); err != nil {
			return core.AttachmentRef{}, err
		}
	}

	display := strings.TrimSpace(src.DisplayName)
	if display == "" {
		display = filepath.Base(abs)
	}
	return core.AttachmentRef{
		Kind:         kind,
		Path:         storedPath,
		OriginalPath: abs,
		MIME:         mime,
		Filename:     filepath.Base(resolved),
		SizeBytes:    info.Size(),
		SHA256:       sum,
		DisplayName:  display,
	}, nil
}

func normalizeOptions(opts Options) Options {
	if opts.MaxAttachments <= 0 {
		opts.MaxAttachments = DefaultMaxAttachments
	}
	if opts.MaxTotalBytes <= 0 {
		opts.MaxTotalBytes = DefaultMaxTotalBytes
	}
	if opts.MaxImageBytes <= 0 {
		opts.MaxImageBytes = DefaultMaxImageBytes
	}
	if opts.MaxPDFBytes <= 0 {
		opts.MaxPDFBytes = DefaultMaxPDFBytes
	}
	if opts.MaxAudioBytes <= 0 {
		opts.MaxAudioBytes = DefaultMaxAudioBytes
	}
	if opts.MaxVideoBytes <= 0 {
		opts.MaxVideoBytes = DefaultMaxVideoBytes
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = DefaultMaxFileBytes
	}
	return opts
}

func classifyAttachment(path string, header []byte) (core.AttachmentKind, string) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if bytesHasPDFHeader(header) || ext == "pdf" {
		return core.AttachmentKindPDF, "application/pdf"
	}
	mime := http.DetectContentType(header)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return core.AttachmentKindImage, mime
	case "audio/mpeg", "audio/wave", "audio/wav", "audio/x-wav", "audio/ogg", "audio/flac", "audio/webm":
		return core.AttachmentKindAudio, mime
	case "video/mp4", "video/quicktime", "video/webm", "video/x-msvideo":
		return core.AttachmentKindVideo, mime
	}
	switch ext {
	case "png":
		return core.AttachmentKindImage, "image/png"
	case "jpg", "jpeg":
		return core.AttachmentKindImage, "image/jpeg"
	case "gif":
		return core.AttachmentKindImage, "image/gif"
	case "webp":
		return core.AttachmentKindImage, "image/webp"
	case "mp3":
		return core.AttachmentKindAudio, "audio/mpeg"
	case "wav":
		return core.AttachmentKindAudio, "audio/wav"
	case "m4a":
		return core.AttachmentKindAudio, "audio/mp4"
	case "ogg":
		return core.AttachmentKindAudio, "audio/ogg"
	case "flac":
		return core.AttachmentKindAudio, "audio/flac"
	case "webm":
		if strings.HasPrefix(mime, "video/") {
			return core.AttachmentKindVideo, mime
		}
		return core.AttachmentKindAudio, "audio/webm"
	case "mp4":
		return core.AttachmentKindVideo, "video/mp4"
	case "mov":
		return core.AttachmentKindVideo, "video/quicktime"
	case "avi":
		return core.AttachmentKindVideo, "video/x-msvideo"
	}
	if mime == "application/octet-stream" && ext != "" {
		return core.AttachmentKindFile, mimeFromExtension(ext)
	}
	return core.AttachmentKindFile, mime
}

func supportedAudioAttachment(mime, path string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch {
	case mime == "audio/mpeg" || ext == "mp3":
		return true
	case mime == "audio/wav" || mime == "audio/wave" || mime == "audio/x-wav" || ext == "wav":
		return true
	default:
		return false
	}
}

func bytesHasPDFHeader(header []byte) bool {
	return len(header) >= 5 && string(header[:5]) == "%PDF-"
}

func maxBytesForKind(kind core.AttachmentKind, opts Options) int64 {
	switch kind {
	case core.AttachmentKindImage:
		return opts.MaxImageBytes
	case core.AttachmentKindPDF:
		return opts.MaxPDFBytes
	case core.AttachmentKindAudio:
		return opts.MaxAudioBytes
	case core.AttachmentKindVideo:
		return opts.MaxVideoBytes
	default:
		return opts.MaxFileBytes
	}
}

func copyPrivateFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open attachment copy source: %w", err)
	}
	defer in.Close()
	if err := securefs.MkdirPrivate(filepath.Dir(dst)); err != nil {
		return fmt.Errorf("prepare attachment copy dir: %w", err)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create stored attachment: %w", err)
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("copy attachment: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close stored attachment: %w", closeErr)
	}
	return nil
}

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func validSessionID(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	return sessionID != "" && sessionIDPattern.MatchString(sessionID) && !strings.Contains(sessionID, "..")
}

var unsafeFilenameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	name = unsafeFilenameChars.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._-")
	if name == "" {
		return "attachment"
	}
	if len(name) > 120 {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if len(ext) > 20 {
			ext = ""
		}
		limit := 120 - len(ext)
		if limit < 1 {
			limit = 120
		}
		if len(base) > limit {
			base = base[:limit]
		}
		name = base + ext
	}
	return name
}

func mimeFromExtension(ext string) string {
	switch ext {
	case "txt", "md", "markdown", "go", "rs", "js", "ts", "tsx", "jsx", "json", "yaml", "yml", "toml", "html", "css", "py", "rb", "java", "kt", "swift", "c", "cpp", "h", "hpp":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

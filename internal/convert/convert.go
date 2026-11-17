// Package convert turns one image or PDF into txt, hocr, or alto with tesseract, pdfinfo, pdftoppm, and pdftotext.
package convert

import (
	"context"
	"strings"
	"time"
)

const (
	EngineOCR                   = "ocr"
	OperationConvert            = "convert"
	DefaultEngineTimeout        = 744 * time.Second
	MaxFileBytes          int64 = 100 << 20
	DefaultMaxPages             = 200
	DefaultMaxImagePixels int64 = 100_000_000
)

// Pair is one source format and one target format.
type Pair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Converter converts the file at inPath into outPath.
type Converter interface {
	Pairs() []Pair
	Convert(ctx context.Context, inPath, outPath string, opts map[string]any) error
}

// MIMEType is the content type the core stores for format.
func MIMEType(format string) string {
	switch NormalizeFormat(format) {
	case "png":
		return "image/png"
	case "jpg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "tiff":
		return "image/tiff"
	case "pdf":
		return "application/pdf"
	case "txt":
		return "text/plain"
	case "hocr":
		return "text/html"
	case "alto":
		return "application/xml"
	default:
		return "application/octet-stream"
	}
}

var formatAliases = map[string]string{
	"jpeg": "jpg",
	"tif":  "tiff",
}

// NormalizeFormat lowercases a format or extension and strips one leading dot.
func NormalizeFormat(f string) string {
	f = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(f), "."))
	if canonical, ok := formatAliases[f]; ok {
		return canonical
	}
	return f
}

type opKey struct {
	Operation string
	From      string
	To        string
}

// Registry maps an operation and a format pair to a converter.
type Registry struct {
	m map[opKey]Converter
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{m: make(map[opKey]Converter)}
}

// Register indexes each pair c reports.
func (r *Registry) Register(c Converter) {
	for _, pair := range c.Pairs() {
		r.m[opKey{
			Operation: OperationConvert,
			From:      NormalizeFormat(pair.From),
			To:        NormalizeFormat(pair.To),
		}] = c
	}
}

// LookupOp reports the converter for op, from, and to.
func (r *Registry) LookupOp(op, from, to string) (Converter, bool) {
	c, ok := r.m[opKey{
		Operation: op,
		From:      NormalizeFormat(from),
		To:        NormalizeFormat(to),
	}]
	return c, ok
}

// Default is the process registry. init registers OCRConverter.
var Default = NewRegistry()

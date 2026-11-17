package convert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ocrPSM             = "3"
	ocrOEM             = "1"
	ocrDefaultLanguage = "eng"
)

var (
	ocrSourceFormats = []string{"jpg", "png", "tiff", "webp", "pdf"}
	ocrTargetFormats = []string{"txt", "hocr", "alto"}
	ocrThreads       int
	active           = &OCRConverter{
		MaxPages:       DefaultMaxPages,
		MaxImagePixels: uint64(DefaultMaxImagePixels),
	}
)

func init() {
	Default.Register(active)
}

// OCRConverter shells out to tesseract, and to poppler for a PDF source.
// A non-empty opts map is rejected. A nil or empty map means -l eng.
type OCRConverter struct {
	MaxImagePixels uint64
	MaxPages       int
	maxOutput      int
	run            func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (c OCRConverter) outputLimit() int {
	if c.maxOutput > 0 {
		return c.maxOutput
	}
	return int(MaxFileBytes)
}

// SetOCRThreads stores the OMP_THREAD_LIMIT passed to tesseract.
func SetOCRThreads(n int) {
	ocrThreads = n
}

// OCRThreadLimit reports the stored OMP_THREAD_LIMIT. Zero means the inherited value, if any.
func OCRThreadLimit() int {
	return ocrThreads
}

// Configure sets the page and pixel caps on the registered converter.
func Configure(maxPages int, maxPixels uint64) {
	active.MaxPages = maxPages
	active.MaxImagePixels = maxPixels
}

// Current returns a copy of the registered converter.
func Current() OCRConverter {
	return *active
}

// Pairs returns every source and target combination, source outer, target inner.
func (OCRConverter) Pairs() []Pair {
	pairs := make([]Pair, 0, len(ocrSourceFormats)*len(ocrTargetFormats))
	for _, from := range ocrSourceFormats {
		for _, to := range ocrTargetFormats {
			pairs = append(pairs, Pair{From: from, To: to})
		}
	}
	return pairs
}

var _ Converter = OCRConverter{}

// tesseract requires imagename, outputbase, then flags, then an optional configfile last.
func ocrTesseractArgs(inPath, outBase string, langArgs []string, configfile string) []string {
	args := []string{inPath, outBase, "--psm", ocrPSM, "--oem", ocrOEM}
	args = append(args, langArgs...)
	if configfile != "" {
		args = append(args, configfile)
	}
	return args
}

func ocrConfigFileFor(target string) (string, bool) {
	switch target {
	case "txt":
		return "", true
	case "hocr":
		return "hocr", true
	case "alto":
		return "alto", true
	default:
		return "", false
	}
}

// ocrTesseractOutputExt is the suffix tesseract appends. The alto config writes outputbase.xml.
func ocrTesseractOutputExt(target string) string {
	switch target {
	case "hocr":
		return ".hocr"
	case "alto":
		return ".xml"
	default:
		return ".txt"
	}
}

func validateOCROutput(path string) error {
	if _, err := statOutput(path); err != nil {
		return fmt.Errorf("ocr: stat output: %w", err)
	}
	return nil
}

func (c OCRConverter) exec(ctx context.Context, jobDir, name string, args ...string) ([]byte, error) {
	if c.run != nil {
		return c.run(ctx, name, args...)
	}
	env := engineEnv(jobDir)
	if name == "pdftotext" {
		return runCapture(ctx, env, c.outputLimit(), name, args...)
	}
	return runCommand(ctx, env, name, args...)
}

// Convert rejects any options map before pdfinfo or tesseract. An empty map uses -l eng.
func (c OCRConverter) Convert(ctx context.Context, inPath, outPath string, opts map[string]any) error {
	if len(opts) != 0 {
		return errors.New("ocr: options are not accepted")
	}
	target := NormalizeFormat(filepath.Ext(outPath))
	configfile, ok := ocrConfigFileFor(target)
	if !ok {
		return fmt.Errorf("ocr: unsupported target format %q", target)
	}
	source := NormalizeFormat(filepath.Ext(inPath))
	if source == "pdf" {
		return c.convertPDF(ctx, inPath, outPath, target, configfile)
	}
	switch source {
	case "jpg", "png", "tiff", "webp":
	default:
		return fmt.Errorf("ocr: unsupported source format %q", source)
	}
	if err := enforceImagePixelBound(inPath, c.MaxImagePixels); err != nil {
		return err
	}
	outBase := strings.TrimSuffix(outPath, filepath.Ext(outPath))
	args := ocrTesseractArgs(inPath, outBase, []string{"-l", ocrDefaultLanguage}, configfile)
	if _, err := c.exec(ctx, filepath.Dir(outPath), "tesseract", args...); err != nil {
		return fmt.Errorf("ocr: tesseract: %w", err)
	}
	produced := outBase + ocrTesseractOutputExt(target)
	if produced != outPath {
		if err := os.Rename(produced, outPath); err != nil {
			return fmt.Errorf("ocr: rename output: %w", err)
		}
	}
	return validateOCROutput(outPath)
}

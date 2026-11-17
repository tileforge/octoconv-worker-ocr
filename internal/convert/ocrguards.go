package convert

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

var (
	ErrOCRTooManyPages    = errors.New("ocr: pdf page count exceeds bound")
	ErrOCRPageBoxTooLarge = errors.New("ocr: pdf page box would rasterize beyond the pixel bound")
	ErrOCRImageTooLarge   = errors.New("ocr: image dimensions exceed bound")
)

const (
	ocrRasterizeDPI             = 300
	ocrUsableTextLayerThreshold = 16
)

func ocrPageCountArgs(pdfPath string) []string {
	return []string{pdfPath}
}

func parsePdfinfoPages(out []byte) (int, error) {
	for _, line := range strings.Split(string(out), "\n") {
		rest, ok := strings.CutPrefix(line, "Pages:")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			return 0, fmt.Errorf("ocr: unparseable pdfinfo Pages line %q: %w", line, err)
		}
		return n, nil
	}
	return 0, errors.New("ocr: pdfinfo output has no Pages line")
}

func enforceMaxPages(count, max int) error {
	if max > 0 && count > max {
		return fmt.Errorf("%w: declared page count %d exceeds bound %d", ErrOCRTooManyPages, count, max)
	}
	return nil
}

func enforcePageBoxBound(widthPts, heightPts float64, dpi int, maxPixels uint64) error {
	widthPx := uint64(math.Round(widthPts / 72 * float64(dpi)))
	heightPx := uint64(math.Round(heightPts / 72 * float64(dpi)))
	pixels := widthPx * heightPx
	if maxPixels > 0 && pixels > maxPixels {
		return fmt.Errorf("%w: declared page box %.0fx%.0f pts at %d DPI rasterizes to %d pixels, exceeds bound %d",
			ErrOCRPageBoxTooLarge, widthPts, heightPts, dpi, pixels, maxPixels)
	}
	return nil
}

func ocrTextLayerPageArgs(pdfPath string, page int) []string {
	p := strconv.Itoa(page)
	return []string{"-f", p, "-l", p, pdfPath, "-"}
}

func countNonWhitespace(b []byte) int {
	count := 0
	for _, r := range string(b) {
		if !unicode.IsSpace(r) {
			count++
		}
	}
	return count
}

func hasUsableTextLayer(nonWhitespace int) bool {
	return nonWhitespace > ocrUsableTextLayerThreshold
}

func enforceImagePixelBound(inPath string, maxPixels uint64) error {
	format := NormalizeFormat(strings.TrimPrefix(filepath.Ext(inPath), "."))
	if !HasDimensionLimit(format) {
		return nil
	}
	f, _, err := OpenJobFile(inPath)
	if err != nil {
		return fmt.Errorf("ocr: open %q: %w", inPath, err)
	}
	defer f.Close()
	if format == "tiff" {
		err := enforceTIFFPixelBound(f, maxPixels)
		if err == nil || errors.Is(err, ErrOCRImageTooLarge) {
			return err
		}
		return fmt.Errorf("ocr: %w", err)
	}
	width, height, err := readDeclaredDimensions(format, f)
	if err != nil {
		return fmt.Errorf("ocr: %w", err)
	}
	pixels := uint64(width) * uint64(height)
	if maxPixels > 0 && pixels > maxPixels {
		return fmt.Errorf("%w: declared %dx%d (%d pixels) exceeds bound %d", ErrOCRImageTooLarge, width, height, pixels, maxPixels)
	}
	return nil
}

type ocrPDFGuardResult struct {
	totalPages int
}

func (c OCRConverter) ocrPDFGuardStage(ctx context.Context, jobDir, pdfPath string) (ocrPDFGuardResult, error) {
	out, err := c.exec(ctx, jobDir, "pdfinfo", ocrPageCountArgs(pdfPath)...)
	if err != nil {
		return ocrPDFGuardResult{}, fmt.Errorf("ocr: pdfinfo: %w", err)
	}
	totalPages, err := parsePdfinfoPages(out)
	if err != nil {
		return ocrPDFGuardResult{}, err
	}
	if err := enforceMaxPages(totalPages, c.MaxPages); err != nil {
		return ocrPDFGuardResult{}, err
	}
	return ocrPDFGuardResult{totalPages: totalPages}, nil
}

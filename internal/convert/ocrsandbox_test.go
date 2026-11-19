package convert

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tileforge/octoconv-worker-ocr/internal/sandbox"
)

func TestOCRGrantsMatchTheMeasuredSet(t *testing.T) {
	if strings.Join(ocrReadOnly, "\n") != strings.Join([]string{
		"/lib",
		"/usr/local/lib",
		"/usr/local/share/tessdata",
		"/etc/ld.so.cache",
		"/etc/localtime",
		"/usr/share/fonts",
		"/sys/devices/system/cpu/possible",
	}, "\n") {
		t.Fatalf("read only %v", ocrReadOnly)
	}
	if strings.Join(ocrEngines, "\n") != "tesseract\npdftoppm\npdfinfo\npdftotext" {
		t.Fatalf("engines %v", ocrEngines)
	}
	if ocrPreflightOutsideBase == "/tmp" || ocrSandboxDisabledEnv != "OCR_SANDBOX_DISABLED" {
		t.Fatal("outside fixture or disable env")
	}
}

func TestWithSandboxEmptyHelperIsUnconfined(t *testing.T) {
	c := OCRConverter{}.WithSandbox(nil)
	if c.sandbox.confines() {
		t.Fatal("confined")
	}
	proof, err := OCRPreflight(t.Context(), c)
	if err != nil || proof.LayerBytes != 0 {
		t.Fatalf("proof %+v err %v", proof, err)
	}
}

func TestForJobGrantsTheJobDirectoryAndEngines(t *testing.T) {
	dir := t.TempDir()
	for _, name := range ocrEngines {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	job := filepath.Join(dir, "job")
	c := OCRConverter{}.WithSandbox([]string{"/usr/local/bin/ocr-worker", sandbox.ExecMode})
	spec := strings.Join(c.jobExec(job).spec, "\n")
	for _, want := range []string{
		"rw:" + job,
		"ro:/lib",
		"ro:/usr/local/share/tessdata",
		"ro:/usr/share/fonts",
		"ro:" + filepath.Join(dir, "tesseract"),
		"ro:" + filepath.Join(dir, "pdftotext"),
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("missing %s in %s", want, spec)
		}
	}
	for _, token := range strings.Split(spec, "\n") {
		if token == "rw:/" || token == "rw:/tmp" || strings.HasPrefix(token, "rw:/tmp/") {
			t.Fatalf("spec grants a shared writable path: %s", spec)
		}
	}
	var bare OCRConverter
	if got := bare.jobExec(job).spec; len(got) != 0 {
		t.Fatalf("unconfined spec %q", got)
	}
}

func TestPreflightCopyAcceptsTheFixtureWithoutChangingTheJobCap(t *testing.T) {
	const low uint64 = 1_000_000
	stub := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "pdfinfo" {
			return []byte("Pages: 1\nPage 1 size: 1000 x 200 pts\n"), nil
		}
		return nil, errors.New("stop at " + name)
	}
	c := OCRConverter{MaxPages: 1, MaxImagePixels: low, run: stub}
	err := c.Convert(t.Context(), "in.pdf", filepath.Join(t.TempDir(), "out.hocr"), nil)
	if !errors.Is(err, ErrOCRPageBoxTooLarge) {
		t.Fatalf("low cap: %v", err)
	}
	raised := c.forPreflight()
	if c.MaxImagePixels != low {
		t.Fatalf("job cap changed to %d", c.MaxImagePixels)
	}
	err = raised.Convert(t.Context(), "in.pdf", filepath.Join(t.TempDir(), "out.hocr"), nil)
	if errors.Is(err, ErrOCRPageBoxTooLarge) || !strings.Contains(err.Error(), "pdftoppm") {
		t.Fatalf("raised cap: %v", err)
	}
	if err := enforcePageBoxBound(ocrPreflightPageWidth, ocrPreflightPageHeight, ocrRasterizeDPI, raised.MaxImagePixels); err != nil {
		t.Fatal(err)
	}
	w := uint64(math.Ceil(float64(ocrPreflightPageWidth) / 72 * float64(ocrRasterizeDPI)))
	h := uint64(math.Ceil(float64(ocrPreflightPageHeight) / 72 * float64(ocrRasterizeDPI)))
	if raised.MaxImagePixels < w*h {
		t.Fatalf("cap %d is below the ceiled raster %d", raised.MaxImagePixels, w*h)
	}

	high := OCRConverter{MaxPages: DefaultMaxPages, MaxImagePixels: uint64(DefaultMaxImagePixels)}
	if got := high.forPreflight(); got.MaxImagePixels != high.MaxImagePixels || got.MaxPages != high.MaxPages {
		t.Fatalf("default cap changed: %+v", got)
	}
	open := OCRConverter{}
	if got := open.forPreflight(); got.MaxImagePixels != ocrPreflightPixels() || open.MaxImagePixels != 0 {
		t.Fatalf("open cap copy %d original %d", got.MaxImagePixels, open.MaxImagePixels)
	}
}

func TestOCRDeniedOutsideRejectsSetupFailureAndAWrittenMarker(t *testing.T) {
	if err := ocrDeniedOutside(ErrEngineSandboxUnavailable, "", []byte("x")); err == nil || !strings.Contains(err.Error(), "sandbox setup failed") {
		t.Fatal(err)
	}
	dir := t.TempDir()
	written := filepath.Join(dir, "neg.txt")
	if err := os.WriteFile(written, []byte("OCTOCONV"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ocrDeniedOutside(errors.New("permission denied"), written, []byte("OCTOCONV")); err == nil || !strings.Contains(err.Error(), "engine wrote") {
		t.Fatal(err)
	}
	if err := ocrDeniedOutside(errors.New("permission denied"), filepath.Join(dir, "missing.txt"), []byte("OCTOCONV")); err != nil {
		t.Fatal(err)
	}
}

package convert

import (
	"errors"
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

package main

import (
	"testing"

	"github.com/tileforge/octoconv-worker-ocr/internal/convert"
)

func TestApplyOCRLimits(t *testing.T) {
	before := convert.Current()
	t.Cleanup(func() {
		convert.Configure(before.MaxPages, before.MaxImagePixels)
	})
	t.Setenv("OCR_MAX_PAGES", "")
	t.Setenv("OCR_MAX_IMAGE_PIXELS", "")
	applyOCRLimits()
	if got := convert.Current(); got.MaxPages != 200 || got.MaxImagePixels != 100_000_000 {
		t.Fatalf("defaults %+v", got)
	}
	registered, ok := convert.Default.LookupOp(convert.OperationConvert, "png", "txt")
	if !ok {
		t.Fatal("png to txt")
	}
	live, ok := registered.(*convert.OCRConverter)
	if !ok || live.MaxPages != 200 || live.MaxImagePixels != 100_000_000 {
		t.Fatalf("registered %T %+v", registered, live)
	}
	t.Setenv("OCR_MAX_PAGES", "3 # pages")
	t.Setenv("OCR_MAX_IMAGE_PIXELS", "10")
	applyOCRLimits()
	if got := convert.Current(); got.MaxPages != 3 || got.MaxImagePixels != 10 {
		t.Fatalf("override %+v", got)
	}
	if live.MaxPages != 3 || live.MaxImagePixels != 10 {
		t.Fatalf("registered converter did not see the override: %+v", live)
	}
}

func TestOCRThreadsFromEnv(t *testing.T) {
	t.Cleanup(func() { convert.SetOCRThreads(0) })
	t.Setenv("OCR_THREADS", "")
	if err := setOCRThreadsFromEnv(); err != nil || convert.OCRThreadLimit() != 0 {
		t.Fatalf("unset %d %v", convert.OCRThreadLimit(), err)
	}
	for _, raw := range []string{"0", "-1", "nope"} {
		t.Setenv("OCR_THREADS", raw)
		if err := setOCRThreadsFromEnv(); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	t.Setenv("OCR_THREADS", "2 # cores")
	if err := setOCRThreadsFromEnv(); err != nil || convert.OCRThreadLimit() != 2 {
		t.Fatalf("threads %d %v", convert.OCRThreadLimit(), err)
	}
}

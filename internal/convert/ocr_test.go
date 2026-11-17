package convert

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPairsMatchManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "schema", "manifest-ocr-convert.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Capabilities []struct {
			Formats []Pair `json:"formats"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	got := (OCRConverter{}).Pairs()
	if len(doc.Capabilities) != 1 || len(got) != 15 || len(doc.Capabilities[0].Formats) != len(got) {
		t.Fatalf("pairs %d manifest %d", len(got), len(doc.Capabilities[0].Formats))
	}
	for i, pair := range got {
		want := doc.Capabilities[0].Formats[i]
		if pair != want {
			t.Fatalf("pair %d %+v want %+v", i, pair, want)
		}
		if _, ok := Default.LookupOp(OperationConvert, pair.From, pair.To); !ok {
			t.Fatalf("lookup %s to %s", pair.From, pair.To)
		}
	}
}

func TestLimits(t *testing.T) {
	if MaxFileBytes != 100<<20 {
		t.Fatalf("max file %d", MaxFileBytes)
	}
	if DefaultEngineTimeout.Seconds() != 744 {
		t.Fatalf("timeout %s", DefaultEngineTimeout)
	}
	if DefaultMaxPages != 200 || DefaultMaxImagePixels != 100_000_000 {
		t.Fatalf("caps %d %d", DefaultMaxPages, DefaultMaxImagePixels)
	}
	cur := Current()
	if cur.MaxPages != DefaultMaxPages || cur.MaxImagePixels != uint64(DefaultMaxImagePixels) {
		t.Fatalf("registered %+v", cur)
	}
}

func TestMIMETypes(t *testing.T) {
	want := map[string]string{
		"txt":  "text/plain",
		"hocr": "text/html",
		"alto": "application/xml",
		"jpg":  "image/jpeg",
		"jpeg": "image/jpeg",
		"png":  "image/png",
		"webp": "image/webp",
		"tiff": "image/tiff",
		"tif":  "image/tiff",
		"pdf":  "application/pdf",
	}
	for format, mime := range want {
		if got := MIMEType(format); got != mime {
			t.Errorf("%s %s", format, got)
		}
	}
}

func TestEngineEnvPassesTesseractVariables(t *testing.T) {
	prev := ocrThreads
	t.Cleanup(func() { ocrThreads = prev })
	t.Setenv("TESSDATA_PREFIX", "/tess")
	t.Setenv("OMP_THREAD_LIMIT", "8")
	t.Setenv("PATH", "/usr/bin")
	ocrThreads = 0
	env := strings.Join(engineEnv("/job"), "\n")
	for _, line := range []string{"TESSDATA_PREFIX=/tess", "OMP_THREAD_LIMIT=8", "PATH=/usr/bin", "TMPDIR=/job"} {
		if !strings.Contains(env, line) {
			t.Fatalf("missing %s in %s", line, env)
		}
	}
	ocrThreads = 2
	env = strings.Join(engineEnv(""), "\n")
	if !strings.Contains(env, "OMP_THREAD_LIMIT=2") || strings.Contains(env, "OMP_THREAD_LIMIT=8") || strings.Contains(env, "TMPDIR=") {
		t.Fatalf("threads env %s", env)
	}
}

func TestTesseractArgsPutConfigLast(t *testing.T) {
	args := ocrTesseractArgs("/in.png", "/out", []string{"-l", "eng"}, "alto")
	want := []string{"/in.png", "/out", "--psm", "3", "--oem", "1", "-l", "eng", "alto"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("args %q", args)
	}
	txt := ocrTesseractArgs("/in.png", "/out", []string{"-l", "eng"}, "")
	if txt[len(txt)-1] == "hocr" || txt[len(txt)-1] == "alto" {
		t.Fatalf("txt args %q", txt)
	}
	if ocrTesseractOutputExt("alto") != ".xml" || ocrTesseractOutputExt("hocr") != ".hocr" || ocrTesseractOutputExt("txt") != ".txt" {
		t.Fatal("extensions")
	}
}

func TestConvertRejectsOptionsBeforeExec(t *testing.T) {
	called := false
	c := OCRConverter{run: func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.png")
	out := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(in, pngIHDR(8, 8), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, opts := range []map[string]any{{"language": "eng"}, {"language": "rus"}, {"language": "eng+rus"}, {"other": 1}} {
		if err := c.Convert(context.Background(), in, out, opts); err == nil || !strings.Contains(err.Error(), "options") {
			t.Fatalf("opts %v err %v", opts, err)
		}
	}
	pdf := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Convert(context.Background(), pdf, filepath.Join(dir, "out.hocr"), map[string]any{"language": ""}); err == nil {
		t.Fatal("empty language key")
	}
	if called {
		t.Fatal("a program ran")
	}
}

func TestImageRejectsPixelsBeforeTesseract(t *testing.T) {
	c := OCRConverter{MaxImagePixels: 100, run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("tesseract")
		return nil, nil
	}}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.png")
	out := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(in, pngIHDR(101, 1), 0o600); err != nil {
		t.Fatal(err)
	}
	err := c.Convert(context.Background(), in, out, nil)
	if !errors.Is(err, ErrOCRImageTooLarge) {
		t.Fatal(err)
	}
	if err := os.WriteFile(in, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = c.Convert(context.Background(), in, out, nil)
	if !errors.Is(err, ErrDimensionsUnknown) {
		t.Fatal(err)
	}
}

func TestImageAcceptsEmptyOutputAndRenamesALTO(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.png")
	out := filepath.Join(dir, "out.alto")
	if err := os.WriteFile(in, pngIHDR(2, 2), 0o600); err != nil {
		t.Fatal(err)
	}
	c := OCRConverter{MaxImagePixels: 100_000_000, run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tesseract" {
			t.Fatalf("program %s", name)
		}
		want := in + " " + strings.TrimSuffix(out, ".alto") + " --psm 3 --oem 1 -l eng alto"
		if strings.Join(args, " ") != want {
			t.Fatalf("args %q", args)
		}
		if err := os.WriteFile(args[1]+".xml", nil, 0o644); err != nil {
			t.Fatal(err)
		}
		return nil, nil
	}}
	if err := c.Convert(context.Background(), in, out, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(out)
	if err != nil || info.Size() != 0 || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("output %v %v", info, err)
	}
	missing := OCRConverter{MaxImagePixels: 100_000_000, run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, nil
	}}
	txt := filepath.Join(dir, "out.txt")
	if err := missing.Convert(context.Background(), in, txt, nil); err == nil {
		t.Fatal("missing output")
	}
}

func TestPDFTextLayerSkipsTesseract(t *testing.T) {
	text := strings.Repeat("a", 17)
	var names []string
	c := pdfConverter(t, &names, func(name string, args []string) ([]byte, error) {
		switch name {
		case "pdfinfo":
			return []byte("Pages: 1\n"), nil
		case "pdftotext":
			if !containsArg(args, "-f", "1") || !containsArg(args, "-l", "1") || args[len(args)-1] != "-" {
				t.Fatalf("pdftotext %q", args)
			}
			return []byte(text), nil
		default:
			t.Fatalf("program %s", name)
			return nil, nil
		}
	})
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.pdf"), out, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(out)
	if err != nil || string(body) != text {
		t.Fatalf("body %q %v", body, err)
	}
	if strings.Join(names, ",") != "pdfinfo,pdftotext" {
		t.Fatalf("calls %v", names)
	}
}

func TestMixedPDFOCRsPagesWithoutText(t *testing.T) {
	cover := strings.Repeat("a", 17)
	var names []string
	c := pdfConverter(t, &names, func(name string, args []string) ([]byte, error) {
		switch name {
		case "pdfinfo":
			if page := argAfter(args, "-f"); page != "" {
				if page != "2" {
					t.Fatalf("pdfinfo page %s", page)
				}
				return []byte("Page    2 size:  612 x 792 pts\n"), nil
			}
			return []byte("Pages: 2\n"), nil
		case "pdftotext":
			switch {
			case containsArg(args, "-f", "1"):
				return []byte(cover), nil
			case containsArg(args, "-f", "2"):
				return []byte("x"), nil
			default:
				t.Fatalf("pdftotext %q", args)
				return nil, nil
			}
		case "pdftoppm":
			assertSinglePage(t, args, 2)
			return nil, os.WriteFile(args[len(args)-1]+".png", []byte("x"), 0o644)
		case "tesseract":
			return nil, os.WriteFile(args[1]+".txt", []byte("scan"), 0o644)
		default:
			t.Fatalf("program %s", name)
			return nil, nil
		}
	})
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.pdf"), out, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(out)
	if err != nil || string(body) != cover+"scan" {
		t.Fatalf("body %q %v", body, err)
	}
	if strings.Join(names, ",") != "pdfinfo,pdftotext,pdftotext,pdfinfo,pdftoppm,tesseract" {
		t.Fatalf("calls %v", names)
	}
}

func TestShortTextLayerIsOCRed(t *testing.T) {
	if hasUsableTextLayer(16) || !hasUsableTextLayer(17) {
		t.Fatal("threshold")
	}
	var names []string
	c := pdfConverter(t, &names, func(name string, args []string) ([]byte, error) {
		switch name {
		case "pdfinfo":
			if containsArg(args, "-f", "1") {
				return []byte("Page    1 size:  612 x 792 pts\n"), nil
			}
			return []byte("Pages: 1\n"), nil
		case "pdftotext":
			return []byte(strings.Repeat("a", 16)), nil
		case "pdftoppm":
			assertSinglePage(t, args, 1)
			return nil, os.WriteFile(args[len(args)-1]+".png", []byte("x"), 0o644)
		case "tesseract":
			return nil, os.WriteFile(args[1]+".txt", []byte("scan"), 0o644)
		default:
			t.Fatalf("program %s", name)
			return nil, nil
		}
	})
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.pdf"), out, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(out)
	if err != nil || string(body) != "scan" {
		t.Fatalf("body %q %v", body, err)
	}
	if strings.Join(names, ",") != "pdfinfo,pdftotext,pdfinfo,pdftoppm,tesseract" {
		t.Fatalf("calls %v", names)
	}
}

func TestHOCRSkipsTextExtractionAndMergesPages(t *testing.T) {
	var names []string
	page := 0
	c := pdfConverter(t, &names, func(name string, args []string) ([]byte, error) {
		switch name {
		case "pdfinfo":
			if containsArg(args, "-f", "1") || containsArg(args, "-f", "2") {
				return []byte("Page    " + argAfter(args, "-f") + " size:  612 x 792 pts\n"), nil
			}
			return []byte("Pages: 2\n"), nil
		case "pdftotext":
			t.Fatal("pdftotext")
			return nil, nil
		case "pdftoppm":
			page++
			assertSinglePage(t, args, page)
			if !strings.Contains(args[len(args)-1], ".ocr-pdf-") {
				t.Fatalf("workdir %q", args[len(args)-1])
			}
			return nil, os.WriteFile(args[len(args)-1]+".png", []byte("x"), 0o644)
		case "tesseract":
			if args[len(args)-1] != "hocr" || !strings.Contains(strings.Join(args, " "), "--psm 3 --oem 1 -l eng hocr") {
				t.Fatalf("args %q", args)
			}
			word := "Hello &amp; co"
			if page == 2 {
				word = "World"
			}
			return nil, os.WriteFile(args[1]+".hocr", hocrFragment(word), 0o644)
		default:
			t.Fatalf("program %s", name)
			return nil, nil
		}
	})
	dir := t.TempDir()
	out := filepath.Join(dir, "out.hocr")
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.pdf"), out, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), "<?xml") != 1 || strings.Count(string(body), "<html") != 1 {
		t.Fatalf("not one document:\n%s", body)
	}
	if !strings.Contains(string(body), `id="page_1"`) || !strings.Contains(string(body), `id="page_2"`) {
		t.Fatalf("ids:\n%s", body)
	}
	if !strings.Contains(string(body), "Hello &amp; co") || strings.Contains(string(body), "&amp;amp;") {
		t.Fatalf("escape:\n%s", body)
	}
	if strings.Contains(strings.Join(names, ","), "pdftotext") {
		t.Fatalf("calls %v", names)
	}
}

func TestPDFALTOReadsTesseractXML(t *testing.T) {
	c := pdfConverter(t, nil, func(name string, args []string) ([]byte, error) {
		switch name {
		case "pdfinfo":
			if containsArg(args, "-f", "1") {
				return []byte("Page    1 size:  612 x 792 pts\n"), nil
			}
			return []byte("Pages: 1\n"), nil
		case "pdftoppm":
			assertSinglePage(t, args, 1)
			return nil, os.WriteFile(args[len(args)-1]+".png", []byte("x"), 0o644)
		case "tesseract":
			if args[len(args)-1] != "alto" {
				t.Fatalf("args %q", args)
			}
			if err := os.WriteFile(args[1]+".alto", []byte("wrong"), 0o644); err != nil {
				t.Fatal(err)
			}
			return nil, os.WriteFile(args[1]+".xml", altoFragment("Hi"), 0o644)
		default:
			t.Fatalf("program %s", name)
			return nil, nil
		}
	})
	dir := t.TempDir()
	out := filepath.Join(dir, "out.alto")
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.pdf"), out, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), "<?xml") != 1 || strings.Count(string(body), "<alto") != 1 || !strings.Contains(string(body), `ID="page_1"`) || !strings.Contains(string(body), "Hi") {
		t.Fatalf("alto:\n%s", body)
	}
}

func TestPageCapAndPageBoxFailBeforeRender(t *testing.T) {
	capped := pdfConverter(t, nil, func(name string, args []string) ([]byte, error) {
		if name != "pdfinfo" {
			t.Fatalf("program %s", name)
		}
		return []byte("Pages: 201\n"), nil
	})
	capped.MaxPages = 200
	dir := t.TempDir()
	err := capped.Convert(context.Background(), filepath.Join(dir, "in.pdf"), filepath.Join(dir, "out.txt"), nil)
	if !errors.Is(err, ErrOCRTooManyPages) {
		t.Fatal(err)
	}
	boxed := pdfConverter(t, nil, func(name string, args []string) ([]byte, error) {
		if name == "pdftoppm" || name == "tesseract" || name == "pdftotext" {
			t.Fatalf("program %s", name)
		}
		if containsArg(args, "-f", "1") {
			return []byte("Page    1 size:  3000 x 3000 pts\n"), nil
		}
		return []byte("Pages: 1\n"), nil
	})
	boxed.MaxPages = 200
	boxed.MaxImagePixels = 100_000_000
	err = boxed.Convert(context.Background(), filepath.Join(dir, "in.pdf"), filepath.Join(dir, "out.hocr"), nil)
	if !errors.Is(err, ErrOCRPageBoxTooLarge) {
		t.Fatal(err)
	}
	open := pdfConverter(t, nil, func(name string, _ []string) ([]byte, error) {
		if name != "pdfinfo" && name != "pdftotext" {
			t.Fatalf("program %s", name)
		}
		if name == "pdftotext" {
			return []byte(strings.Repeat("a", 17)), nil
		}
		return []byte("Pages: 500\n"), nil
	})
	open.MaxPages = 0
	out := filepath.Join(dir, "open.txt")
	if err := open.Convert(context.Background(), filepath.Join(dir, "in.pdf"), out, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPdftotextKeepsTheStartAndRejectsPastTheCeiling(t *testing.T) {
	dir := t.TempDir()
	payload := append([]byte("HEAD"), bytes.Repeat([]byte("a"), 1<<20)...)
	payload = append(payload, []byte("TAIL")...)
	data := filepath.Join(dir, "layer.txt")
	if err := os.WriteFile(data, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "pdftotext")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n/bin/cat \""+data+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	c := OCRConverter{}
	out, err := c.exec(context.Background(), dir, "pdftotext", "in.pdf", "-")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, []byte("HEAD")) || !bytes.HasSuffix(out, []byte("TAIL")) || len(out) != len(payload) {
		t.Fatalf("len %d prefix %q suffix %q", len(out), out[:4], out[len(out)-4:])
	}
	c.maxOutput = 8
	if _, err := c.exec(context.Background(), dir, "pdftotext", "in.pdf", "-"); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatal(err)
	}
}

func TestTextLayerOverTheCeilingIsRejected(t *testing.T) {
	c := pdfConverter(t, nil, func(name string, _ []string) ([]byte, error) {
		if name == "pdftotext" {
			return []byte(strings.Repeat("a", 17)), nil
		}
		return []byte("Pages: 1\n"), nil
	})
	c.maxOutput = 4
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	err := c.Convert(context.Background(), filepath.Join(dir, "in.pdf"), out, nil)
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatal(err)
	}
	if _, statErr := os.Lstat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal(statErr)
	}
}

func TestPageAccumulationStopsAtTheCeiling(t *testing.T) {
	var calls int
	c := pdfConverter(t, nil, func(name string, args []string) ([]byte, error) {
		switch name {
		case "pdfinfo":
			if containsArg(args, "-f", "1") || containsArg(args, "-f", "2") {
				return []byte("Page    " + argAfter(args, "-f") + " size:  612 x 792 pts\n"), nil
			}
			return []byte("Pages: 2\n"), nil
		case "pdftotext":
			return []byte("short"), nil
		case "pdftoppm":
			return nil, os.WriteFile(args[len(args)-1]+".png", []byte("x"), 0o644)
		case "tesseract":
			calls++
			return nil, os.WriteFile(args[1]+".txt", []byte("123456"), 0o644)
		default:
			t.Fatalf("program %s", name)
			return nil, nil
		}
	})
	c.maxOutput = 10
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	err := c.Convert(context.Background(), filepath.Join(dir, "in.pdf"), out, nil)
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("tesseract calls %d", calls)
	}
	if _, statErr := os.Lstat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal(statErr)
	}
}

func TestMergedDocumentOverTheCeilingIsNotWritten(t *testing.T) {
	fragment := hocrFragment("Hi")
	c := pdfConverter(t, nil, func(name string, args []string) ([]byte, error) {
		switch name {
		case "pdfinfo":
			if containsArg(args, "-f", "1") {
				return []byte("Page    1 size:  612 x 792 pts\n"), nil
			}
			return []byte("Pages: 1\n"), nil
		case "pdftoppm":
			return nil, os.WriteFile(args[len(args)-1]+".png", []byte("x"), 0o644)
		case "tesseract":
			return nil, os.WriteFile(args[1]+".hocr", fragment, 0o644)
		default:
			t.Fatalf("program %s", name)
			return nil, nil
		}
	})
	c.maxOutput = len(fragment)
	dir := t.TempDir()
	out := filepath.Join(dir, "out.hocr")
	err := c.Convert(context.Background(), filepath.Join(dir, "in.pdf"), out, nil)
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatal(err)
	}
	if _, statErr := os.Lstat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal(statErr)
	}
}

func TestMergePrefixesDescendantIDs(t *testing.T) {
	page := []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body>
<div class="ocr_page" id="page_1"><span class="ocrx_word" id="word_1_1">A &amp; B</span></div>
</body></html>`)
	m := newOCRPageMerger("hocr")
	if err := m.AddPage(1, page); err != nil {
		t.Fatal(err)
	}
	if err := m.AddPage(2, page); err != nil {
		t.Fatal(err)
	}
	out, err := m.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	if !strings.Contains(body, `id="p1_word_1_1"`) || !strings.Contains(body, `id="p2_word_1_1"`) || strings.Contains(body, `id="word_1_1"`) {
		t.Fatalf("hocr ids:\n%s", body)
	}
	if !strings.Contains(body, "A &amp; B") || strings.Contains(body, "&amp;amp;") {
		t.Fatalf("hocr escape:\n%s", body)
	}

	alto := []byte(`<alto xmlns="http://www.loc.gov/standards/alto/ns-v3#"><Layout>
<Page ID="page_1"><TextLine ID="line_1" IDNEXT="line_2"><String ID="word_1" CONTENT="word_1" STYLEREFS="font_0"/></TextLine><TextLine ID="line_2"/></Page>
</Layout></alto>`)
	altoMerge := newOCRPageMerger("alto")
	if err := altoMerge.AddPage(2, alto); err != nil {
		t.Fatal(err)
	}
	out, err = altoMerge.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	body = string(out)
	for _, want := range []string{`ID="p2_line_1"`, `ID="p2_line_2"`, `ID="p2_word_1"`, `IDNEXT="p2_line_2"`, `CONTENT="word_1"`, `STYLEREFS="font_0"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in\n%s", want, body)
		}
	}
	if strings.Contains(body, `ID="line_1"`) || strings.Contains(body, `ID="word_1"`) {
		t.Fatalf("alto ids:\n%s", body)
	}
}

func TestGoSourcesDoNotNameLandlock(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(bytes.ToLower(body), []byte("landlock")) {
			t.Fatalf("%s names landlock", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeclaredDimensions(t *testing.T) {
	w, h, ok := pngDimensions(pngIHDR(100, 40))
	if !ok || w != 100 || h != 40 {
		t.Fatalf("png %d %d %v", w, h, ok)
	}
	if _, _, ok = pngDimensions([]byte("short")); ok {
		t.Fatal("short png")
	}
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xC4, 0x00, 0x02, 0xFF, 0xC0, 0x00, 0x0B, 0x08, 0x00, 0x02, 0x00, 0x03}
	w, h, ok = jpegDimensions(jpeg)
	if !ok || w != 3 || h != 2 {
		t.Fatalf("jpeg %d %d %v", w, h, ok)
	}
	w, h, err := readDeclaredDimensions("jpg", bytes.NewReader(jpeg))
	if err != nil || w != 3 || h != 2 {
		t.Fatalf("jpeg reader %d %d %v", w, h, err)
	}
	webp := make([]byte, 30)
	copy(webp[0:4], []byte("RIFF"))
	copy(webp[8:12], []byte("WEBP"))
	copy(webp[12:16], []byte("VP8X"))
	webp[24], webp[27] = 9, 19
	w, h, ok = webpDimensions(webp)
	if !ok || w != 10 || h != 20 {
		t.Fatalf("webp %d %d %v", w, h, ok)
	}
	tiff := []byte{
		'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00,
		0x02, 0x00,
		0x00, 0x01, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00, 0x0A, 0x00, 0x00, 0x00,
		0x01, 0x01, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00, 0x14, 0x00, 0x00, 0x00,
	}
	w, h, ok = tiffDimensions(tiff)
	if !ok || w != 10 || h != 20 {
		t.Fatalf("tiff %d %d %v", w, h, ok)
	}
	w, h, err = readDeclaredDimensions("tiff", bytes.NewReader(tiff))
	if err != nil || w != 10 || h != 20 {
		t.Fatalf("tiff reader %d %d %v", w, h, err)
	}
	if HasDimensionLimit("heic") || HasDimensionLimit("pdf") {
		t.Fatal("unexpected parser")
	}
}

func TestJPEGDimensionsPastThePeek(t *testing.T) {
	const comLen = 65533
	buf := make([]byte, 2+2+comLen+9)
	buf[0], buf[1] = 0xFF, 0xD8
	buf[2], buf[3] = 0xFF, 0xFE
	buf[4], buf[5] = 0xFF, 0xFD
	sof := len(buf) - 9
	copy(buf[sof:], []byte{0xFF, 0xC0, 0x00, 0x0B, 0x08, 0x00, 0x10, 0x00, 0x10})
	if sof <= DimensionPeekBytes {
		t.Fatalf("sof at %d", sof)
	}
	if _, _, ok := jpegDimensions(buf[:DimensionPeekBytes]); ok {
		t.Fatal("prefix holds no SOF")
	}
	w, h, err := readDeclaredDimensions("jpg", bytes.NewReader(buf))
	if err != nil || w != 16 || h != 16 {
		t.Fatalf("%d %d %v", w, h, err)
	}
	path := filepath.Join(t.TempDir(), "page.jpg")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := enforceImagePixelBound(path, 100_000_000); err != nil {
		t.Fatal(err)
	}
	short := []byte{0xFF, 0xD8, 0xFF, 0xFE, 0xFF, 0xFF}
	if _, _, err := readDeclaredDimensions("jpg", bytes.NewReader(short)); !errors.Is(err, ErrDimensionsUnknown) {
		t.Fatalf("truncated: %v", err)
	}
}

func TestTIFFDirectoryPastThePeek(t *testing.T) {
	for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		payload := tiffWithDirectoryAt(bo, 256, 256, 65544)
		if len(payload) <= DimensionPeekBytes {
			t.Fatalf("fixture length %d", len(payload))
		}
		if _, _, ok := tiffDimensions(payload[:DimensionPeekBytes]); ok {
			t.Fatal("prefix holds no directory")
		}
		w, h, err := readDeclaredDimensions("tiff", bytes.NewReader(payload))
		if err != nil || w != 256 || h != 256 {
			t.Fatalf("%T: %d %d %v", bo, w, h, err)
		}
	}
	payload := tiffWithDirectoryAt(binary.LittleEndian, 256, 256, 65544)
	path := filepath.Join(t.TempDir(), "page.tiff")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := enforceImagePixelBound(path, 100_000_000); err != nil {
		t.Fatal(err)
	}
	if err := enforceImagePixelBound(path, 10); !errors.Is(err, ErrOCRImageTooLarge) {
		t.Fatalf("bound: %v", err)
	}
}

func TestTIFFDirectoryPastTheFileFailsClosed(t *testing.T) {
	short := make([]byte, 8)
	short[0], short[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(short[2:4], 42)
	binary.LittleEndian.PutUint32(short[4:8], 65544)
	_, _, err := readDeclaredDimensions("tiff", bytes.NewReader(short))
	if !errors.Is(err, ErrDimensionsUnknown) {
		t.Fatalf("offset: %v", err)
	}

	trunc := make([]byte, 10)
	trunc[0], trunc[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(trunc[2:4], 42)
	binary.LittleEndian.PutUint32(trunc[4:8], 8)
	binary.LittleEndian.PutUint16(trunc[8:10], 10)
	_, _, err = readDeclaredDimensions("tiff", bytes.NewReader(trunc))
	if !errors.Is(err, ErrDimensionsUnknown) {
		t.Fatalf("count: %v", err)
	}
}

func tiffWithDirectoryAt(bo binary.ByteOrder, width, height uint32, ifdAt int) []byte {
	const dirLen = 2 + 2*12
	buf := make([]byte, ifdAt+dirLen)
	if bo == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	bo.PutUint16(buf[2:4], 42)
	bo.PutUint32(buf[4:8], uint32(ifdAt))
	bo.PutUint16(buf[ifdAt:ifdAt+2], 2)
	putTIFFEntry(bo, buf[ifdAt+2:], 256, 3, width)
	putTIFFEntry(bo, buf[ifdAt+14:], 257, 3, height)
	return buf
}

func putTIFFEntry(bo binary.ByteOrder, b []byte, tag, typ uint16, value uint32) {
	bo.PutUint16(b[0:2], tag)
	bo.PutUint16(b[2:4], typ)
	bo.PutUint32(b[4:8], 1)
	if typ == 3 {
		bo.PutUint16(b[8:10], uint16(value))
		return
	}
	bo.PutUint32(b[8:12], value)
}

func pdfConverter(t *testing.T, names *[]string, fn func(name string, args []string) ([]byte, error)) OCRConverter {
	t.Helper()
	return OCRConverter{
		MaxPages:       200,
		MaxImagePixels: 100_000_000,
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if names != nil {
				*names = append(*names, name)
			}
			return fn(name, append([]string(nil), args...))
		},
	}
}

func assertSinglePage(t *testing.T, args []string, page int) {
	t.Helper()
	pageStr := strconv.Itoa(page)
	if !containsArg(args, "-r", "300") || !containsArg(args, "-f", pageStr) || !containsArg(args, "-l", pageStr) || !hasArg(args, "-singlefile") || !hasArg(args, "-png") {
		t.Fatalf("pdftoppm %q", args)
	}
}

func containsArg(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func hasArg(args []string, key string) bool {
	for _, arg := range args {
		if arg == key {
			return true
		}
	}
	return false
}

func argAfter(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}

func pngIHDR(w, h uint32) []byte {
	b := make([]byte, 24)
	copy(b, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	binary.BigEndian.PutUint32(b[8:12], 13)
	copy(b[12:16], []byte("IHDR"))
	binary.BigEndian.PutUint32(b[16:20], w)
	binary.BigEndian.PutUint32(b[20:24], h)
	return b
}

func hocrFragment(word string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><body>
<div class="ocr_page" id="page_1"><span class="ocrx_word">` + word + `</span></div>
</body></html>`)
}

func altoFragment(word string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<alto xmlns="http://www.loc.gov/standards/alto/ns-v3#"><Layout>
<Page ID="page_1"><PrintSpace><String CONTENT="` + word + `"/></PrintSpace></Page>
</Layout></alto>`)
}

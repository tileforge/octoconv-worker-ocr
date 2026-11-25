package convert

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestOCRRenderManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "schema", "manifest-ocr-render.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Capabilities []struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
			Legacy  *struct {
				Operation string `json:"operation"`
				Engine    string `json:"engine"`
			} `json:"legacy"`
			ClientOperation string `json:"client_operation"`
			Inputs          []struct {
				Role string `json:"role"`
				Min  int    `json:"min"`
				Max  int    `json:"max"`
			} `json:"inputs"`
			Outputs []struct {
				Role string `json:"role"`
				Min  int    `json:"min"`
				Max  int    `json:"max"`
			} `json:"outputs"`
			Schema  json.RawMessage `json:"parameter_schema"`
			Formats []Pair          `json:"formats"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Capabilities) != 1 {
		t.Fatalf("capabilities %d", len(doc.Capabilities))
	}
	cap := doc.Capabilities[0]
	if cap.ID != "octoconv.render.ocr" || cap.Version != 1 {
		t.Fatalf("capability %s version %d", cap.ID, cap.Version)
	}
	if cap.Legacy == nil || cap.Legacy.Operation != OperationRender || cap.Legacy.Engine != EngineOCR {
		t.Fatalf("legacy %+v", cap.Legacy)
	}
	if cap.ClientOperation != OperationRender {
		t.Fatalf("client operation %s", cap.ClientOperation)
	}
	if len(cap.Inputs) != 1 || cap.Inputs[0].Role != "source" || cap.Inputs[0].Min != 1 || cap.Inputs[0].Max != 1 {
		t.Fatalf("inputs %+v", cap.Inputs)
	}
	if len(cap.Outputs) != 1 || cap.Outputs[0].Role != "primary" || cap.Outputs[0].Min != 1 || cap.Outputs[0].Max != 1 {
		t.Fatalf("outputs %+v", cap.Outputs)
	}
	if !bytes.Equal(bytes.TrimSpace(cap.Schema), []byte("{}")) {
		t.Fatalf("schema %s", cap.Schema)
	}
	want := (OCRRenderConverter{}).Pairs()
	if len(cap.Formats) != len(want) {
		t.Fatalf("formats %d", len(cap.Formats))
	}
	for i := range want {
		if cap.Formats[i] != want[i] || want[i].To != "png" {
			t.Fatalf("pair %d = %+v, want %+v", i, cap.Formats[i], want[i])
		}
		conv, ok := Default.LookupOp(OperationRender, want[i].From, "png")
		if !ok {
			t.Fatalf("missing render %s", want[i].From)
		}
		if _, isRender := conv.(OCRRenderConverter); !isRender {
			t.Fatalf("render converter %T", conv)
		}
	}
	conv, ok := Default.LookupOp(OperationConvert, "jpg", "txt")
	if !ok {
		t.Fatal("missing convert jpg to txt")
	}
	if _, isOCR := conv.(*OCRConverter); !isOCR {
		t.Fatalf("convert converter %T", conv)
	}
	if MIMEType("png") != "image/png" {
		t.Fatalf("mime %s", MIMEType("png"))
	}
	defer func() {
		if recover() != nil {
			t.Fatal("register panicked")
		}
	}()
	Default.Register(OCRRenderConverter{})
}

func TestOCRRenderFirstContent(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.pdf")
	out := filepath.Join(dir, "out.png")
	if err := os.WriteFile(in, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var names []string
	var pages []int
	conv := OCRRenderConverter{OCRConverter: OCRConverter{
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			names = append(names, name)
			switch name {
			case "pdfinfo":
				return []byte("Pages: 3\n"), nil
			case "pdftoppm":
				page, err := renderPageArg(args, 256)
				if err != nil {
					return nil, err
				}
				pages = append(pages, page)
				if hasArg(args, "-cropbox") || hasArg(args, "-r") {
					return nil, fmt.Errorf("argv %q", args)
				}
				prefix := args[len(args)-1]
				img := image.NewNRGBA(image.Rect(0, 0, 256, 128))
				fillNRGBA(img, color.NRGBA{255, 255, 255, 255})
				if page == 3 {
					for x := 122; x < 134; x++ {
						img.SetNRGBA(x, 64, color.NRGBA{0, 0, 0, 255})
					}
				}
				if err := writePNG(prefix+".png", img); err != nil {
					return nil, err
				}
				return nil, nil
			default:
				return nil, fmt.Errorf("program %s", name)
			}
		},
	}}
	if err := conv.Convert(context.Background(), in, out, map[string]any{"size": 256}); err != nil {
		t.Fatal(err)
	}
	if !sameInts(pages, []int{1, 2, 3}) {
		t.Fatalf("pages %v", pages)
	}
	for _, name := range names {
		if name == "tesseract" || name == "pdftotext" {
			t.Fatalf("program %s", name)
		}
	}
	img := decodePNG(t, out)
	if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 256 {
		t.Fatalf("size %v", img.Bounds())
	}
	if r, g, b := rgbAt(img, 128, 128); whitePix(r, g, b) {
		t.Fatal("center is white")
	}
}

func TestOCRRenderLetterbox(t *testing.T) {
	wide := filepath.Join(t.TempDir(), "wide.pdf")
	if err := os.WriteFile(wide, vectorPagePDF(400, 200, 100, 200), 0o600); err != nil {
		t.Fatal(err)
	}
	wideOut := filepath.Join(filepath.Dir(wide), "out.png")
	if err := (OCRRenderConverter{}).Convert(context.Background(), wide, wideOut, map[string]any{"size": 256}); err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, wideOut)
	assertNonWhiteBox(t, img, 0, 64, 256, 128)
	assertCornersWhite(t, img)
	if r, g, b := rgbAt(img, 128, 128); whitePix(r, g, b) {
		t.Fatal("wide center is white")
	}
	if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(wide), "fit.png")); len(matches) != 0 {
		t.Fatal("fit.png")
	}

	tallDir := t.TempDir()
	tall := filepath.Join(tallDir, "tall.pdf")
	if err := os.WriteFile(tall, vectorPagePDF(20, 40, 20, 40), 0o600); err != nil {
		t.Fatal(err)
	}
	tallOut := filepath.Join(tallDir, "out.png")
	if err := (OCRRenderConverter{}).Convert(context.Background(), tall, tallOut, map[string]any{"size": 256}); err != nil {
		t.Fatal(err)
	}
	img = decodePNG(t, tallOut)
	assertNonWhiteBox(t, img, 64, 0, 128, 256)
	assertCornersWhite(t, img)
	if r, g, b := rgbAt(img, 128, 128); whitePix(r, g, b) {
		t.Fatal("tall center is white")
	}
}

func TestOCRRenderUnserved(t *testing.T) {
	if _, ok := Default.LookupOp(OperationRender, "gif", "png"); ok {
		t.Fatal("gif is served")
	}
	if _, ok := Default.LookupOp(OperationRender, "pdf", "jpg"); ok {
		t.Fatal("pdf to jpg is served")
	}
	for _, pair := range [][2]string{{"gif", "png"}, {"pdf", "jpg"}} {
		dir := t.TempDir()
		in := filepath.Join(dir, "in."+pair[0])
		out := filepath.Join(dir, "out."+pair[1])
		if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		precreate(t, out)
		var calls int
		conv := OCRRenderConverter{OCRConverter: OCRConverter{
			run: func(context.Context, string, ...string) ([]byte, error) {
				calls++
				return nil, fmt.Errorf("called")
			},
		}}
		if err := conv.Convert(context.Background(), in, out, nil); err == nil {
			t.Fatalf("%s to %s succeeded", pair[0], pair[1])
		}
		if calls != 0 {
			t.Fatalf("%s calls %d", pair[0], calls)
		}
		assertGone(t, out)
	}
}

func TestOCRRenderCleanSheet(t *testing.T) {
	clean := []struct {
		name string
		img  *image.NRGBA
	}{
		{"fifteen specks", speckSheet(15)},
		{"sixteen specks", speckSheet(16)},
		{"seven cluster", clusterSheet(7)},
		{"twelve absent", frameSheet()},
	}
	for _, tc := range clean {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "in.png")
			out := filepath.Join(dir, "out.png")
			if err := writePNG(in, tc.img); err != nil {
				t.Fatal(err)
			}
			var calls int
			conv := OCRRenderConverter{OCRConverter: OCRConverter{
				run: func(context.Context, string, ...string) ([]byte, error) {
					calls++
					return nil, fmt.Errorf("called")
				},
			}}
			if err := conv.Convert(context.Background(), in, out, map[string]any{"size": 256}); err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatalf("calls %d", calls)
			}
			assertWhiteSquare(t, decodePNG(t, out), 256)
		})
	}
	for _, tc := range []struct {
		name string
		img  *image.NRGBA
	}{
		{"seventeen specks", speckSheet(17)},
		{"eight cluster", clusterSheet(8)},
		{"twelve run", runSheet(12)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "in.png")
			out := filepath.Join(dir, "out.png")
			if err := writePNG(in, tc.img); err != nil {
				t.Fatal(err)
			}
			if err := (OCRRenderConverter{}).Convert(context.Background(), in, out, map[string]any{"size": 256}); err != nil {
				t.Fatal(err)
			}
			img := decodePNG(t, out)
			if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 256 {
				t.Fatalf("size %v", img.Bounds())
			}
			if allWhite(img) {
				t.Fatal("content page stored white")
			}
		})
	}
}

func TestOCRRenderAllClean(t *testing.T) {
	for _, edge := range []int{256, 400} {
		t.Run(strconv.Itoa(edge), func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "in.pdf")
			out := filepath.Join(dir, "out.png")
			if err := os.WriteFile(in, []byte("%PDF-1.4\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var pages []int
			conv := OCRRenderConverter{OCRConverter: OCRConverter{
				run: func(_ context.Context, name string, args ...string) ([]byte, error) {
					switch name {
					case "pdfinfo":
						return []byte("Pages: 2\n"), nil
					case "pdftoppm":
						page, err := renderPageArg(args, edge)
						if err != nil {
							return nil, err
						}
						pages = append(pages, page)
						if err := writePNG(args[len(args)-1]+".png", noisyClean(edge)); err != nil {
							return nil, err
						}
						return nil, nil
					default:
						return nil, fmt.Errorf("program %s", name)
					}
				},
			}}
			if err := conv.Convert(context.Background(), in, out, map[string]any{"size": edge}); err != nil {
				t.Fatal(err)
			}
			if !sameInts(pages, []int{1, 2}) {
				t.Fatalf("pages %v", pages)
			}
			assertWhiteSquare(t, decodePNG(t, out), edge)
		})
	}
}

func TestOCRRenderUndrawable(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.pdf")
	out := filepath.Join(dir, "out.png")
	if err := os.WriteFile(in, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	precreate(t, out)
	var pages []int
	conv := OCRRenderConverter{OCRConverter: OCRConverter{
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "pdfinfo" {
				return []byte("Pages: 3\n"), nil
			}
			if name != "pdftoppm" {
				return nil, fmt.Errorf("program %s", name)
			}
			page, err := renderPageArg(args, 256)
			if err != nil {
				return nil, err
			}
			pages = append(pages, page)
			if page == 2 {
				return nil, fmt.Errorf("pdftoppm failed")
			}
			if page == 3 {
				return nil, fmt.Errorf("page 3 was rasterized")
			}
			if err := writePNG(args[len(args)-1]+".png", whiteSheet(256, 128)); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}}
	if err := conv.Convert(context.Background(), in, out, map[string]any{"size": 256}); err == nil {
		t.Fatal("undrawable page succeeded")
	}
	if !sameInts(pages, []int{1, 2}) {
		t.Fatalf("pages %v", pages)
	}
	assertGone(t, out)
}

func TestOCRRenderPageBound(t *testing.T) {
	if DefaultMaxPages != 200 {
		t.Fatalf("bound %d", DefaultMaxPages)
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.pdf")
	out := filepath.Join(dir, "out.png")
	if err := os.WriteFile(in, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	precreate(t, out)
	var pdftoppm int
	conv := OCRRenderConverter{OCRConverter: OCRConverter{
		run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "pdftoppm" {
				pdftoppm++
			}
			if name == "pdfinfo" {
				return []byte("Pages: 201\n"), nil
			}
			return nil, fmt.Errorf("program %s", name)
		},
	}}
	if err := conv.Convert(context.Background(), in, out, nil); err == nil {
		t.Fatal("201 pages succeeded")
	}
	if pdftoppm != 0 {
		t.Fatalf("pdftoppm calls %d", pdftoppm)
	}
	assertGone(t, out)
}

func TestOCRRenderSize(t *testing.T) {
	accept := []any{256, int32(256), int64(256), float64(256), 2048, int32(2048), int64(2048), float64(2048)}
	for _, size := range accept {
		dir := t.TempDir()
		in := filepath.Join(dir, "in.pdf")
		out := filepath.Join(dir, "out.png")
		if err := os.WriteFile(in, []byte("%PDF-1.4\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var scale string
		conv := OCRRenderConverter{OCRConverter: OCRConverter{
			run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "pdfinfo" {
					return []byte("Pages: 1\n"), nil
				}
				if name != "pdftoppm" {
					return nil, fmt.Errorf("program %s", name)
				}
				scale = argAfter(args, "-scale-to")
				edge, err := strconv.Atoi(scale)
				if err != nil {
					return nil, err
				}
				if err := writePNG(args[len(args)-1]+".png", whiteSheet(edge, max(1, edge/2))); err != nil {
					return nil, err
				}
				return nil, nil
			},
		}}
		if err := conv.Convert(context.Background(), in, out, map[string]any{"size": size}); err != nil {
			t.Fatalf("%v: %v", size, err)
		}
		want := "256"
		if n, ok := wholeTestEdge(size); ok && n == 2048 {
			want = "2048"
		}
		if scale != want {
			t.Fatalf("%v scale %s", size, scale)
		}
	}
	for _, opts := range []map[string]any{nil, {}} {
		dir := t.TempDir()
		in := filepath.Join(dir, "in.pdf")
		out := filepath.Join(dir, "out.png")
		if err := os.WriteFile(in, []byte("%PDF-1.4\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var scale string
		conv := OCRRenderConverter{OCRConverter: OCRConverter{
			run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "pdfinfo" {
					return []byte("Pages: 1\n"), nil
				}
				scale = argAfter(args, "-scale-to")
				if err := writePNG(args[len(args)-1]+".png", whiteSheet(1280, 640)); err != nil {
					return nil, err
				}
				return nil, nil
			},
		}}
		if err := conv.Convert(context.Background(), in, out, opts); err != nil {
			t.Fatal(err)
		}
		if scale != "1280" {
			t.Fatalf("%#v scale %s", opts, scale)
		}
	}
	reject := []map[string]any{
		{"size": 255},
		{"size": 2049},
		{"size": 256.5},
		{"size": math.NaN()},
		{"size": math.Inf(1)},
		{"size": "256"},
		{"size": true},
		{"language": "eng"},
		{"size": 256, "language": "eng"},
	}
	for _, opts := range reject {
		dir := t.TempDir()
		in := filepath.Join(dir, "in.pdf")
		out := filepath.Join(dir, "out.png")
		if err := os.WriteFile(in, []byte("%PDF-1.4\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		precreate(t, out)
		var calls int
		conv := OCRRenderConverter{OCRConverter: OCRConverter{
			run: func(context.Context, string, ...string) ([]byte, error) {
				calls++
				return nil, fmt.Errorf("called")
			},
		}}
		if err := conv.Convert(context.Background(), in, out, opts); err == nil {
			t.Fatalf("%#v accepted", opts)
		}
		if calls != 0 {
			t.Fatalf("%#v calls %d", opts, calls)
		}
		assertGone(t, out)
	}
}

func TestOCRRenderBlackPage(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.pdf")
	out := filepath.Join(dir, "out.png")
	if err := os.WriteFile(in, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	conv := OCRRenderConverter{OCRConverter: OCRConverter{
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "pdfinfo" {
				return []byte("Pages: 1\n"), nil
			}
			img := solidSheet(256, 128, color.NRGBA{0, 0, 0, 255})
			if err := writePNG(args[len(args)-1]+".png", img); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}}
	if err := conv.Convert(context.Background(), in, out, map[string]any{"size": 256}); err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, out)
	if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 256 {
		t.Fatalf("size %v", img.Bounds())
	}
	if r, g, b := rgbAt(img, 128, 128); whitePix(r, g, b) {
		t.Fatal("center is white")
	}
	if r, g, b := rgbAt(img, 0, 0); !whitePix(r, g, b) {
		t.Fatalf("corner %d,%d,%d", r, g, b)
	}
	if allWhite(img) {
		t.Fatal("black page stored white")
	}
}

func TestOCRRenderRasterFit(t *testing.T) {
	dir := t.TempDir()
	jpg := filepath.Join(dir, "in.jpg")
	out := filepath.Join(dir, "out.png")
	if err := writeJPEG(jpg, solidSheet(40, 20, color.NRGBA{200, 10, 10, 255})); err != nil {
		t.Fatal(err)
	}
	if err := (OCRRenderConverter{}).Convert(context.Background(), jpg, out, map[string]any{"size": 256}); err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, out)
	assertNonWhiteBox(t, img, 0, 64, 256, 128)
	assertCornersWhite(t, img)
	if r, g, b := rgbAt(img, 128, 128); whitePix(r, g, b) {
		t.Fatal("jpeg center is white")
	}

	for _, tc := range []struct {
		name string
		img  *image.NRGBA
	}{
		{"noise", speckSheet(15)},
		{"transparent", uniformSheet(color.NRGBA{10, 20, 30, 0})},
		{"alpha 14", uniformSheet(color.NRGBA{0, 0, 0, 14})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "in.png")
			out := filepath.Join(dir, "out.png")
			if err := writePNG(in, tc.img); err != nil {
				t.Fatal(err)
			}
			var calls int
			conv := OCRRenderConverter{OCRConverter: OCRConverter{
				run: func(context.Context, string, ...string) ([]byte, error) {
					calls++
					return nil, fmt.Errorf("called")
				},
			}}
			if err := conv.Convert(context.Background(), in, out, map[string]any{"size": 256}); err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatalf("calls %d", calls)
			}
			assertWhiteSquare(t, decodePNG(t, out), 256)
		})
	}

	grayDir := t.TempDir()
	grayIn := filepath.Join(grayDir, "in.png")
	grayOut := filepath.Join(grayDir, "out.png")
	if err := writePNG(grayIn, solidSheet(40, 20, color.NRGBA{0, 0, 0, 15})); err != nil {
		t.Fatal(err)
	}
	if err := (OCRRenderConverter{}).Convert(context.Background(), grayIn, grayOut, map[string]any{"size": 256}); err != nil {
		t.Fatal(err)
	}
	gray := decodePNG(t, grayOut)
	if gray.Bounds().Dx() != 256 || gray.Bounds().Dy() != 256 {
		t.Fatalf("gray size %v", gray.Bounds())
	}
	for y := 64; y < 192; y++ {
		for x := 0; x < 256; x++ {
			r, g, b := rgbAt(gray, x, y)
			if r != 240 || g != 240 || b != 240 {
				t.Fatalf("(%d,%d) %d,%d,%d", x, y, r, g, b)
			}
		}
	}
	assertCornersWhite(t, gray)

	bandDir := t.TempDir()
	bandIn := filepath.Join(bandDir, "in.png")
	bandOut := filepath.Join(bandDir, "out.png")
	edgeBand := solidSheet(40, 20, color.NRGBA{10, 20, 30, 0})
	for y := 0; y < 2; y++ {
		for x := 0; x < 40; x++ {
			edgeBand.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 255})
		}
	}
	edgeDir := t.TempDir()
	edgeIn := filepath.Join(edgeDir, "in.png")
	edgeOut := filepath.Join(edgeDir, "out.png")
	if err := writePNG(edgeIn, edgeBand); err != nil {
		t.Fatal(err)
	}
	var edgeCalls int
	edgeConv := OCRRenderConverter{OCRConverter: OCRConverter{
		run: func(context.Context, string, ...string) ([]byte, error) {
			edgeCalls++
			return nil, fmt.Errorf("called")
		},
	}}
	if err := edgeConv.Convert(context.Background(), edgeIn, edgeOut, map[string]any{"size": 256}); err != nil {
		t.Fatal(err)
	}
	if edgeCalls != 0 {
		t.Fatalf("edge calls %d", edgeCalls)
	}
	assertWhiteSquare(t, decodePNG(t, edgeOut), 256)

	band := solidSheet(40, 20, color.NRGBA{10, 20, 30, 0})
	for y := 2; y < 4; y++ {
		for x := 0; x < 40; x++ {
			band.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 255})
		}
	}
	if err := writePNG(bandIn, band); err != nil {
		t.Fatal(err)
	}
	if err := (OCRRenderConverter{}).Convert(context.Background(), bandIn, bandOut, map[string]any{"size": 256}); err != nil {
		t.Fatal(err)
	}
	got := decodePNG(t, bandOut)
	for y := 76; y <= 88; y++ {
		for _, x := range []int{0, 128, 255} {
			r, g, b := rgbAt(got, x, y)
			if r != 0 || g != 0 || b != 0 {
				t.Fatalf("(%d,%d) %d,%d,%d", x, y, r, g, b)
			}
		}
	}
	if r, g, b := rgbAt(got, 128, 75); !whitePix(r, g, b) {
		t.Fatalf("row 75 %d,%d,%d", r, g, b)
	}
	if r, g, b := rgbAt(got, 128, 89); !whitePix(r, g, b) {
		t.Fatalf("row 89 %d,%d,%d", r, g, b)
	}
	assertCornersWhite(t, got)
	if r, g, b := rgbAt(got, 128, 128); !whitePix(r, g, b) {
		t.Fatalf("center %d,%d,%d", r, g, b)
	}
}

func TestOCRRenderImagePixelBound(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.png")
	out := filepath.Join(dir, "out.png")
	if err := os.WriteFile(in, pngIHDR(11, 10), 0o600); err != nil {
		t.Fatal(err)
	}
	precreate(t, out)
	var calls int
	conv := OCRRenderConverter{OCRConverter: OCRConverter{
		MaxImagePixels: 100,
		run: func(context.Context, string, ...string) ([]byte, error) {
			calls++
			return nil, fmt.Errorf("called")
		},
	}}
	err := conv.Convert(context.Background(), in, out, nil)
	if !errors.Is(err, ErrOCRImageTooLarge) {
		t.Fatalf("%v", err)
	}
	if calls != 0 {
		t.Fatalf("calls %d", calls)
	}
	assertGone(t, out)
}

func TestOCRRenderTIFFOnePicture(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.tiff")
	out := filepath.Join(dir, "out.png")
	if err := os.WriteFile(in, tiffPages(binary.LittleEndian, [][2]uint32{{32, 32}, {32, 32}}), 0o600); err != nil {
		t.Fatal(err)
	}
	precreate(t, out)
	var names []string
	conv := OCRRenderConverter{OCRConverter: OCRConverter{
		run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			names = append(names, name)
			return nil, fmt.Errorf("called")
		},
	}}
	if err := conv.Convert(context.Background(), in, out, nil); err == nil {
		t.Fatal("tiff succeeded")
	}
	if len(names) != 0 {
		t.Fatalf("programs %v", names)
	}
	for _, name := range names {
		if name == "tesseract" {
			t.Fatal("tesseract")
		}
	}
	if !pairsContain((OCRRenderConverter{}).Pairs(), "tiff", "png") {
		t.Fatal("tiff pair missing")
	}
	assertGone(t, out)

	webDir := t.TempDir()
	webIn := filepath.Join(webDir, "in.webp")
	webOut := filepath.Join(webDir, "out.png")
	if err := os.WriteFile(webIn, []byte("not a picture"), 0o600); err != nil {
		t.Fatal(err)
	}
	precreate(t, webOut)
	names = nil
	if err := conv.Convert(context.Background(), webIn, webOut, nil); err == nil {
		t.Fatal("webp succeeded")
	}
	if len(names) != 0 {
		t.Fatalf("webp programs %v", names)
	}
	if !pairsContain((OCRRenderConverter{}).Pairs(), "webp", "png") {
		t.Fatal("webp pair missing")
	}
	assertGone(t, webOut)
}

func renderPageArg(args []string, edge int) (int, error) {
	if len(args) != 10 || args[0] != "-f" || args[2] != "-l" || args[1] != args[3] || args[4] != "-singlefile" || args[5] != "-png" || args[6] != "-scale-to" || args[7] != strconv.Itoa(edge) {
		return 0, fmt.Errorf("argv %q", args)
	}
	return strconv.Atoi(args[1])
}

func vectorPagePDF(mediaW, mediaH, cropW, cropH int) []byte {
	stream := fmt.Sprintf("1 0 0 rg\n0 0 %d %d re\nf\n", mediaW, mediaH)
	objs := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Count 1 /Kids [3 0 R] >>"),
		fmt.Appendf(nil, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /CropBox [0 0 %d %d] /Contents 4 0 R >>", mediaW, mediaH, cropW, cropH),
		append([]byte(fmt.Sprintf("<< /Length %d >>\nstream\n%s", len(stream), stream)), []byte("endstream")...),
	}
	return pdfDocument(objs)
}

func speckSheet(n int) *image.NRGBA {
	img := frameSheet()
	for i := 0; i < n; i++ {
		img.SetNRGBA(4+i*3, 4, color.NRGBA{230, 230, 230, 255})
	}
	return img
}

func clusterSheet(n int) *image.NRGBA {
	img := frameSheet()
	for i := 0; i < n; i++ {
		img.SetNRGBA(10+i, 10, color.NRGBA{0, 0, 0, 255})
	}
	return img
}

func runSheet(n int) *image.NRGBA {
	return clusterSheet(n)
}

func frameSheet() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 32))
	fillNRGBA(img, color.NRGBA{255, 255, 255, 255})
	for y := 0; y < 32; y++ {
		for x := 0; x < 64; x++ {
			if x < 2 || y < 2 || x >= 62 || y >= 30 {
				img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 255})
			}
		}
	}
	return img
}

func uniformSheet(c color.NRGBA) *image.NRGBA {
	return solidSheet(64, 32, c)
}

func solidSheet(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	fillNRGBA(img, c)
	return img
}

func whiteSheet(w, h int) *image.NRGBA {
	return solidSheet(w, h, color.NRGBA{255, 255, 255, 255})
}

func noisyClean(edge int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, edge, edge/2))
	fillNRGBA(img, color.NRGBA{255, 255, 255, 255})
	h := img.Bounds().Dy()
	w := img.Bounds().Dx()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < 2 || y < 2 || x >= w-2 || y >= h-2 {
				img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 255})
			}
		}
	}
	for i := 0; i < 8; i++ {
		img.SetNRGBA(8+i*5, 8, color.NRGBA{230, 230, 230, 255})
	}
	return img
}

func fillNRGBA(img *image.NRGBA, c color.NRGBA) {
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func writeJPEG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	err = jpeg.Encode(f, img, &jpeg.Options{Quality: 100})
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func decodePNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func rgbAt(img image.Image, x, y int) (uint8, uint8, uint8) {
	r, g, b, _ := img.At(x, y).RGBA()
	return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
}

func whitePix(r, g, b uint8) bool {
	return r == 255 && g == 255 && b == 255
}

func allWhite(img image.Image) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if r, g, bl := rgbAt(img, x, y); !whitePix(r, g, bl) {
				return false
			}
		}
	}
	return true
}

func assertWhiteSquare(t *testing.T, img image.Image, edge int) {
	t.Helper()
	if img.Bounds().Dx() != edge || img.Bounds().Dy() != edge || !allWhite(img) {
		t.Fatalf("white square %v", img.Bounds())
	}
}

func assertCornersWhite(t *testing.T, img image.Image) {
	t.Helper()
	b := img.Bounds()
	for _, p := range [][2]int{{0, 0}, {b.Dx() - 1, 0}, {0, b.Dy() - 1}, {b.Dx() - 1, b.Dy() - 1}} {
		r, g, bl := rgbAt(img, p[0], p[1])
		if !whitePix(r, g, bl) {
			t.Fatalf("corner (%d,%d) %d,%d,%d", p[0], p[1], r, g, bl)
		}
	}
}

func assertNonWhiteBox(t *testing.T, img image.Image, x, y, w, h int) {
	t.Helper()
	b := img.Bounds()
	minX, minY := b.Dx(), b.Dy()
	maxX, maxY := -1, -1
	for yy := 0; yy < b.Dy(); yy++ {
		for xx := 0; xx < b.Dx(); xx++ {
			r, g, bl := rgbAt(img, xx, yy)
			if whitePix(r, g, bl) {
				continue
			}
			if xx < minX {
				minX = xx
			}
			if yy < minY {
				minY = yy
			}
			if xx > maxX {
				maxX = xx
			}
			if yy > maxY {
				maxY = yy
			}
		}
	}
	if minX != x || minY != y || maxX-minX+1 != w || maxY-minY+1 != h {
		t.Fatalf("box (%d,%d) %dx%d", minX, minY, maxX-minX+1, maxY-minY+1)
	}
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("output remains: %v", err)
	}
}

func precreate(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sameInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func pairsContain(pairs []Pair, from, to string) bool {
	for _, pair := range pairs {
		if pair.From == from && pair.To == to {
			return true
		}
	}
	return false
}

func wholeTestEdge(size any) (int, bool) {
	switch n := size.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

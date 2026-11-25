package convert

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

func init() {
	Default.Register(OCRRenderConverter{})
}

const (
	ocrRenderDefault = 1280
	ocrRenderMin     = 256
	ocrRenderMax     = 2048
	ocrInkY          = 240
)

// OCRRenderConverter draws one source page as a primary square PNG.
type OCRRenderConverter struct {
	OCRConverter
}

var _ Converter = OCRRenderConverter{}

// Operation reports the render operation.
func (OCRRenderConverter) Operation() string { return OperationRender }

// Pairs returns jpg, png, tiff, webp, and pdf, each to png.
func (OCRRenderConverter) Pairs() []Pair {
	pairs := make([]Pair, 0, len(ocrSourceFormats))
	for _, from := range ocrSourceFormats {
		pairs = append(pairs, Pair{From: from, To: "png"})
	}
	return pairs
}

// Convert writes one square PNG at outPath.
// A nil or empty opts map uses edge 1280. The only accepted key is size.
func (c OCRRenderConverter) Convert(ctx context.Context, inPath, outPath string, opts map[string]any) error {
	if err := c.draw(ctx, inPath, outPath, opts); err != nil {
		_ = os.Remove(outPath)
		return err
	}
	return nil
}

func (c OCRRenderConverter) draw(ctx context.Context, inPath, outPath string, opts map[string]any) error {
	edge, err := ocrRenderEdge(opts)
	if err != nil {
		return err
	}
	from := NormalizeFormat(filepath.Ext(inPath))
	to := NormalizeFormat(filepath.Ext(outPath))
	if !ocrRenderPair(from, to) {
		return fmt.Errorf("ocr: unsupported conversion %s -> %s", from, to)
	}
	switch from {
	case "tiff", "webp":
		return fmt.Errorf("ocr: cannot draw %s", from)
	case "jpg", "png":
		return c.renderImage(ctx, from, inPath, outPath, edge)
	case "pdf":
		return c.renderPDF(ctx, inPath, outPath, edge)
	default:
		return fmt.Errorf("ocr: unsupported conversion %s -> %s", from, to)
	}
}

func ocrRenderPair(from, to string) bool {
	if to != "png" {
		return false
	}
	for _, src := range ocrSourceFormats {
		if src == from {
			return true
		}
	}
	return false
}

func ocrRenderEdge(opts map[string]any) (int, error) {
	if len(opts) == 0 {
		return ocrRenderDefault, nil
	}
	if len(opts) != 1 {
		return 0, fmt.Errorf("ocr: unsupported render option")
	}
	raw, ok := opts["size"]
	if !ok {
		return 0, fmt.Errorf("ocr: unsupported render option")
	}
	edge, ok := ocrWholeEdge(raw)
	if !ok || edge < ocrRenderMin || edge > ocrRenderMax {
		return 0, fmt.Errorf("ocr: render size %v", raw)
	}
	return edge, nil
}

func ocrWholeEdge(raw any) (int, bool) {
	switch n := raw.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		if int64(int(n)) != n {
			return 0, false
		}
		return int(n), true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
			return 0, false
		}
		if n > math.MaxInt || n < math.MinInt {
			return 0, false
		}
		return int(n), true
	default:
		return 0, false
	}
}

func (c OCRRenderConverter) imagePixelLimit() uint64 {
	if c.MaxImagePixels > 0 {
		return c.MaxImagePixels
	}
	host := c.host()
	if host.MaxImagePixels > 0 {
		return host.MaxImagePixels
	}
	conv, ok := Default.LookupOp(OperationConvert, "jpg", "txt")
	if !ok {
		return 0
	}
	switch found := conv.(type) {
	case *OCRConverter:
		if found != nil {
			return found.MaxImagePixels
		}
	case OCRConverter:
		return found.MaxImagePixels
	}
	return 0
}

func (c OCRRenderConverter) host() OCRConverter {
	if c.run != nil || c.sandbox != nil {
		return c.OCRConverter
	}
	conv, ok := Default.LookupOp(OperationConvert, "jpg", "txt")
	if !ok {
		return c.OCRConverter
	}
	var host OCRConverter
	switch found := conv.(type) {
	case OCRConverter:
		host = found
	case *OCRConverter:
		if found == nil {
			return c.OCRConverter
		}
		host = *found
	default:
		return c.OCRConverter
	}
	if host.run != nil || host.sandbox != nil {
		return host
	}
	return c.OCRConverter
}

func (c OCRRenderConverter) renderPDF(ctx context.Context, inPath, outPath string, edge int) error {
	host := c.host()
	jobDir := filepath.Dir(outPath)
	out, err := host.exec(ctx, jobDir, "pdfinfo", ocrPageCountArgs(inPath)...)
	if err != nil {
		return err
	}
	count, err := parsePdfinfoPages(out)
	if err != nil {
		return err
	}
	if err := enforceMaxPages(count, DefaultMaxPages); err != nil {
		return err
	}
	for page := 1; page <= count; page++ {
		kept, err := c.drawPDFPage(ctx, host, jobDir, inPath, outPath, page, edge)
		if err != nil {
			return err
		}
		if kept {
			return nil
		}
	}
	return writeWhiteSquare(outPath, edge)
}

func (c OCRRenderConverter) renderImage(ctx context.Context, from, inPath, outPath string, edge int) error {
	if err := enforceImagePixelBound(inPath, c.imagePixelLimit()); err != nil {
		return err
	}
	img, err := decodeRenderImage(from, inPath)
	if err != nil {
		return err
	}
	if !pageHasContent(img) {
		return writeWhiteSquare(outPath, edge)
	}
	pdf, err := imagePDF(img)
	if err != nil {
		return err
	}
	jobDir := filepath.Dir(outPath)
	pdfPath, err := writeTempPDF(jobDir, inPath, outPath, pdf)
	if err != nil {
		return err
	}
	defer os.Remove(pdfPath)
	host := c.host()
	kept, err := c.drawPDFPage(ctx, host, jobDir, pdfPath, outPath, 1, edge)
	if err != nil {
		return err
	}
	if !kept {
		return writeWhiteSquare(outPath, edge)
	}
	return nil
}

func decodeRenderImage(from, path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	switch from {
	case "jpg":
		return jpeg.Decode(f)
	case "png":
		return png.Decode(f)
	default:
		return nil, fmt.Errorf("ocr: cannot draw %s", from)
	}
}

func (c OCRRenderConverter) drawPDFPage(ctx context.Context, host OCRConverter, jobDir, pdfPath, outPath string, page, edge int) (bool, error) {
	prefix, err := renderPrefix(jobDir, pdfPath, outPath)
	if err != nil {
		return false, err
	}
	pngPath := prefix + ".png"
	defer os.Remove(pngPath)
	if _, err := host.exec(ctx, jobDir, "pdftoppm", ocrRenderPageArgs(pdfPath, prefix, page, edge)...); err != nil {
		return false, err
	}
	f, err := os.Open(pngPath)
	if err != nil {
		return false, err
	}
	img, decErr := png.Decode(f)
	closeErr := f.Close()
	if decErr != nil {
		return false, decErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err := rasterMeetsEdge(img, edge); err != nil {
		return false, err
	}
	if !pageHasContent(img) {
		return false, nil
	}
	boxed, err := letterbox(img, edge)
	if err != nil {
		return false, err
	}
	if err := writePNG(outPath, boxed); err != nil {
		return false, err
	}
	return true, nil
}

func ocrRenderPageArgs(pdfPath, prefix string, page, edge int) []string {
	p := strconv.Itoa(page)
	return []string{
		"-f", p,
		"-l", p,
		"-singlefile",
		"-png",
		"-scale-to", strconv.Itoa(edge),
		pdfPath,
		prefix,
	}
}

func renderPrefix(dir, inPath, outPath string) (string, error) {
	f, err := os.CreateTemp(dir, ".ocr-render-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return "", closeErr
	}
	if removeErr != nil {
		return "", removeErr
	}
	pngPath := name + ".png"
	if name == outPath || name == inPath || pngPath == outPath || pngPath == inPath {
		_ = os.Remove(name)
		_ = os.Remove(pngPath)
		return "", fmt.Errorf("ocr: render temp path collides")
	}
	return name, nil
}

func flatten8(c color.Color) (r, g, b uint8) {
	rr, gg, bb, a := c.RGBA()
	r = uint8((rr + (0xffff - a)) >> 8)
	g = uint8((gg + (0xffff - a)) >> 8)
	b = uint8((bb + (0xffff - a)) >> 8)
	return r, g, b
}

func isInk(r, g, b uint8) bool {
	y := (299*int(r) + 587*int(g) + 114*int(b)) / 1000
	return y <= ocrInkY
}

func borderBand(w, h int) int {
	band := min(w, h) / 64
	if band < 2 {
		band = 2
	}
	if w <= 2*band || h <= 2*band {
		return 0
	}
	return band
}

func pageHasContent(img image.Image) bool {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return false
	}
	band := borderBand(w, h)
	x0, y0 := band, band
	x1, y1 := w-band, h-band
	interiorN := (x1 - x0) * (y1 - y0)
	ink := make([]bool, w*h)
	count := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			r, g, bl := flatten8(img.At(b.Min.X+x, b.Min.Y+y))
			if isInk(r, g, bl) {
				ink[y*w+x] = true
				count++
			}
		}
	}
	if interiorN > 0 && int64(count)*100 >= int64(interiorN) {
		return true
	}
	for y := y0; y < y1; y++ {
		run := 0
		for x := x0; x < x1; x++ {
			if ink[y*w+x] {
				run++
				if run >= 12 {
					return true
				}
			} else {
				run = 0
			}
		}
	}
	for x := x0; x < x1; x++ {
		run := 0
		for y := y0; y < y1; y++ {
			if ink[y*w+x] {
				run++
				if run >= 12 {
					return true
				}
			} else {
				run = 0
			}
		}
	}
	seen := make([]bool, w*h)
	stack := make([]int, 0, 8)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			start := y*w + x
			if !ink[start] || seen[start] {
				continue
			}
			stack = append(stack[:0], start)
			seen[start] = true
			size := 0
			for len(stack) > 0 {
				cur := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				size++
				if size >= 8 {
					return true
				}
				cx, cy := cur%w, cur/w
				for _, step := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					nx, ny := cx+step[0], cy+step[1]
					if nx < x0 || ny < y0 || nx >= x1 || ny >= y1 {
						continue
					}
					next := ny*w + nx
					if ink[next] && !seen[next] {
						seen[next] = true
						stack = append(stack, next)
					}
				}
			}
		}
	}
	return false
}

func rasterMeetsEdge(img image.Image, edge int) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	longer := w
	if h > longer {
		longer = h
	}
	if longer != edge || w > edge || h > edge {
		return fmt.Errorf("ocr: raster %dx%d does not meet edge %d", w, h, edge)
	}
	return nil
}

func letterbox(img image.Image, edge int) (*image.NRGBA, error) {
	if err := rasterMeetsEdge(img, edge); err != nil {
		return nil, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, edge, edge))
	for i := 0; i < len(dst.Pix); i += 4 {
		dst.Pix[i] = 255
		dst.Pix[i+1] = 255
		dst.Pix[i+2] = 255
		dst.Pix[i+3] = 255
	}
	ox := (edge - w) / 2
	oy := (edge - h) / 2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(ox+x, oy+y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst, nil
}

func writeWhiteSquare(path string, edge int) error {
	dst := image.NewNRGBA(image.Rect(0, 0, edge, edge))
	for i := 0; i < len(dst.Pix); i += 4 {
		dst.Pix[i] = 255
		dst.Pix[i+1] = 255
		dst.Pix[i+2] = 255
		dst.Pix[i+3] = 255
	}
	return writePNG(path, dst)
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	err = png.Encode(f, img)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func imagePDF(img image.Image) ([]byte, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	samples := make([]byte, w*h*3)
	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl := flatten8(img.At(x, y))
			samples[i] = r
			samples[i+1] = g
			samples[i+2] = bl
			i += 3
		}
	}
	return buildImagePDF(w, h, samples)
}

func buildImagePDF(w, h int, samples []byte) ([]byte, error) {
	if len(samples) != w*h*3 {
		return nil, fmt.Errorf("ocr: pdf sample length %d", len(samples))
	}
	content := fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im Do Q\n", w, h)
	objs := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Count 1 /Kids [3 0 R] >>"),
		fmt.Appendf(nil, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /CropBox [0 0 %d %d] /Contents 4 0 R /Resources << /XObject << /Im 5 0 R >> >> >>", w, h, w, h),
		append([]byte(fmt.Sprintf("<< /Length %d >>\nstream\n%s", len(content), content)), []byte("endstream")...),
		append(append([]byte(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Length %d >>\nstream\n", w, h, len(samples))), samples...), []byte("\nendstream")...),
	}
	return pdfDocument(objs), nil
}

func pdfDocument(objs [][]byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, obj := range objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n", i+1)
		buf.Write(obj)
		buf.WriteString("\nendobj\n")
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objs)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return buf.Bytes()
}

func writeTempPDF(dir, inPath, outPath string, pdf []byte) (string, error) {
	f, err := os.CreateTemp(dir, ".ocr-render-*.pdf")
	if err != nil {
		return "", err
	}
	name := f.Name()
	_, werr := f.Write(pdf)
	cerr := f.Close()
	if werr != nil || cerr != nil || name == inPath || name == outPath {
		_ = os.Remove(name)
		if werr != nil {
			return "", werr
		}
		if cerr != nil {
			return "", cerr
		}
		return "", fmt.Errorf("ocr: render temp path collides")
	}
	return name, nil
}

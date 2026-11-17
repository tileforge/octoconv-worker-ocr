package convert

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func ocrRasterizePageArgs(pdfPath, outBase string, page, dpi int) []string {
	return []string{
		"-r", strconv.Itoa(dpi),
		"-f", strconv.Itoa(page),
		"-l", strconv.Itoa(page),
		"-singlefile",
		"-png",
		pdfPath, outBase,
	}
}

func ocrPDFPageSizeArgs(pdfPath string, page int) []string {
	return []string{"-f", strconv.Itoa(page), "-l", strconv.Itoa(page), pdfPath}
}

// pdfinfo -f N -l N prints "Page    N size:" rather than "Page size:".
func parsePdfinfoPerPagePageSize(out []byte, page int) (float64, float64, bool) {
	pageStr := strconv.Itoa(page)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 || fields[0] != "Page" || fields[1] != pageStr || fields[2] != "size:" {
			continue
		}
		if fields[4] != "x" || fields[6] != "pts" {
			return 0, 0, false
		}
		w, err := strconv.ParseFloat(fields[3], 64)
		if err != nil {
			continue
		}
		h, err := strconv.ParseFloat(fields[5], 64)
		if err != nil {
			continue
		}
		return w, h, true
	}
	return 0, 0, false
}

func ocrPageWorkDir(outPath string) (string, error) {
	dir, err := os.MkdirTemp(filepath.Dir(outPath), ".ocr-pdf-")
	if err != nil {
		return "", fmt.Errorf("ocr: pdf tmpdir: %w", err)
	}
	return dir, nil
}

func (c OCRConverter) ocrPDFPage(ctx context.Context, jobDir, pdfPath, tmpDir string, page, remain int, configfile, targetFormat string) ([]byte, error) {
	sizeOut, err := c.exec(ctx, jobDir, "pdfinfo", ocrPDFPageSizeArgs(pdfPath, page)...)
	if err != nil {
		return nil, fmt.Errorf("ocr: pdfinfo: %w", err)
	}
	wPts, hPts, ok := parsePdfinfoPerPagePageSize(sizeOut, page)
	if !ok {
		return nil, fmt.Errorf("ocr: pdfinfo: no page-box size for page %d", page)
	}
	if err := enforcePageBoxBound(wPts, hPts, ocrRasterizeDPI, c.MaxImagePixels); err != nil {
		return nil, err
	}
	pageImageBase := filepath.Join(tmpDir, "page")
	if _, err := c.exec(ctx, jobDir, "pdftoppm", ocrRasterizePageArgs(pdfPath, pageImageBase, page, ocrRasterizeDPI)...); err != nil {
		return nil, fmt.Errorf("ocr: pdftoppm: %w", err)
	}
	defer os.Remove(pageImageBase + ".png")
	pageOutBase := filepath.Join(tmpDir, "pageout")
	args := ocrTesseractArgs(pageImageBase+".png", pageOutBase, []string{"-l", ocrDefaultLanguage}, configfile)
	if _, err := c.exec(ctx, jobDir, "tesseract", args...); err != nil {
		return nil, fmt.Errorf("ocr: tesseract: %w", err)
	}
	produced := pageOutBase + ocrTesseractOutputExt(targetFormat)
	defer os.Remove(produced)
	fragment, err := readCapped(produced, remain)
	if err != nil {
		return nil, err
	}
	return fragment, nil
}

func (c OCRConverter) convertPDF(ctx context.Context, inPath, outPath, targetFormat, configfile string) error {
	jobDir := filepath.Dir(outPath)
	guard, err := c.ocrPDFGuardStage(ctx, jobDir, inPath, targetFormat)
	if err != nil {
		return err
	}
	if guard.done {
		if len(guard.text) > c.outputLimit() {
			return ErrOutputTooLarge
		}
		if err := writeNewFile(outPath, guard.text, 0o644); err != nil {
			return fmt.Errorf("ocr: write output: %w", err)
		}
		return validateOCROutput(outPath)
	}
	tmpDir, err := ocrPageWorkDir(outPath)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	var merger *ocrPageMerger
	if configfile != "" {
		merger = newOCRPageMerger(targetFormat)
	}
	var textBuf bytes.Buffer
	retained := 0
	limit := c.outputLimit()
	for page := 1; page <= guard.totalPages; page++ {
		remain := limit - retained
		if remain < 0 {
			return ErrOutputTooLarge
		}
		fragment, err := c.ocrPDFPage(ctx, jobDir, inPath, tmpDir, page, remain, configfile, targetFormat)
		if err != nil {
			return err
		}
		if len(fragment) > remain {
			return ErrOutputTooLarge
		}
		retained += len(fragment)
		if merger != nil {
			if err := merger.AddPage(page, fragment); err != nil {
				return fmt.Errorf("ocr: %w", err)
			}
			continue
		}
		textBuf.Write(fragment)
	}
	finalBytes := textBuf.Bytes()
	if merger != nil {
		finalBytes, err = merger.Finalize()
		if err != nil {
			return fmt.Errorf("ocr: %w", err)
		}
		if len(finalBytes) > limit {
			return ErrOutputTooLarge
		}
	}
	if err := writeNewFile(outPath, finalBytes, 0o644); err != nil {
		return fmt.Errorf("ocr: write output: %w", err)
	}
	return validateOCROutput(outPath)
}

type ocrPageMerger struct {
	target string
	pages  []ocrXMLNode
}

type ocrXMLNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Inner   string     `xml:",innerxml"`
}

func newOCRPageMerger(target string) *ocrPageMerger {
	return &ocrPageMerger{target: target}
}

func ocrPageElementMatch(target string, se xml.StartElement) bool {
	switch target {
	case "hocr":
		if se.Name.Local != "div" {
			return false
		}
		for _, a := range se.Attr {
			if a.Name.Local == "class" && a.Value == "ocr_page" {
				return true
			}
		}
		return false
	case "alto":
		return se.Name.Local == "Page"
	default:
		return false
	}
}

func ocrPageIDAttr(target string) string {
	if target == "alto" {
		return "ID"
	}
	return "id"
}

func setOCRAttr(attrs []xml.Attr, local, value string) []xml.Attr {
	for i, a := range attrs {
		if a.Name.Local == local {
			attrs[i].Value = value
			return attrs
		}
	}
	return append(attrs, xml.Attr{Name: xml.Name{Local: local}, Value: value})
}

func (m *ocrPageMerger) AddPage(pageNum int, fragment []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(fragment))
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return fmt.Errorf("ocr: merge: page %d: no %s page element found", pageNum, m.target)
			}
			return fmt.Errorf("ocr: merge: page %d: %w", pageNum, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || !ocrPageElementMatch(m.target, se) {
			continue
		}
		var node ocrXMLNode
		if err := dec.DecodeElement(&node, &se); err != nil {
			return fmt.Errorf("ocr: merge: page %d: decode page element: %w", pageNum, err)
		}
		node.Attrs = setOCRAttr(node.Attrs, ocrPageIDAttr(m.target), "page_"+strconv.Itoa(pageNum))
		m.pages = append(m.pages, node)
		return nil
	}
}

const (
	ocrHOCRXmlns = "http://www.w3.org/1999/xhtml"
	ocrALTOXmlns = "http://www.loc.gov/standards/alto/ns-v3#"
)

type ocrHOCRDoc struct {
	XMLName xml.Name    `xml:"html"`
	Xmlns   string      `xml:"xmlns,attr"`
	Body    ocrHOCRBody `xml:"body"`
}

type ocrHOCRBody struct {
	Pages []ocrXMLNode `xml:"div"`
}

type ocrALTODoc struct {
	XMLName xml.Name      `xml:"alto"`
	Xmlns   string        `xml:"xmlns,attr"`
	Layout  ocrALTOLayout `xml:"Layout"`
}

type ocrALTOLayout struct {
	Pages []ocrXMLNode `xml:"Page"`
}

func (m *ocrPageMerger) Finalize() ([]byte, error) {
	var doc any
	switch m.target {
	case "hocr":
		doc = ocrHOCRDoc{Xmlns: ocrHOCRXmlns, Body: ocrHOCRBody{Pages: m.pages}}
	case "alto":
		doc = ocrALTODoc{Xmlns: ocrALTOXmlns, Layout: ocrALTOLayout{Pages: m.pages}}
	default:
		return nil, fmt.Errorf("ocr: merge: unsupported target %q", m.target)
	}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("ocr: merge: marshal: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}

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

// pdfinfo reports the CropBox. pdftoppm renders the MediaBox unless -cropbox is set.
func ocrRasterizePageArgs(pdfPath, outBase string, page, dpi int) []string {
	return []string{
		"-r", strconv.Itoa(dpi),
		"-f", strconv.Itoa(page),
		"-l", strconv.Itoa(page),
		"-cropbox",
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

func (c OCRConverter) ocrPDFPage(ctx context.Context, jobDir, pdfPath, tmpDir string, page, remain int, configfile, targetFormat string, langArgs []string) ([]byte, error) {
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
	args := ocrTesseractArgs(pageImageBase+".png", pageOutBase, langArgs, configfile)
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

func (c OCRConverter) convertPDF(ctx context.Context, inPath, outPath, targetFormat, configfile string, langArgs []string) error {
	jobDir := filepath.Dir(outPath)
	guard, err := c.ocrPDFGuardStage(ctx, jobDir, inPath)
	if err != nil {
		return err
	}
	var tmpDir string
	defer func() {
		if tmpDir != "" {
			os.RemoveAll(tmpDir)
		}
	}()

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
		if merger == nil {
			text, err := c.exec(ctx, jobDir, "pdftotext", ocrTextLayerPageArgs(inPath, page)...)
			if err != nil {
				return fmt.Errorf("ocr: pdftotext: %w", err)
			}
			if hasUsableTextLayer(countNonWhitespace(text)) {
				if len(text) > remain {
					return ErrOutputTooLarge
				}
				retained += len(text)
				textBuf.Write(text)
				continue
			}
		}
		if tmpDir == "" {
			tmpDir, err = ocrPageWorkDir(outPath)
			if err != nil {
				return err
			}
		}
		fragment, err := c.ocrPDFPage(ctx, jobDir, inPath, tmpDir, page, remain, configfile, targetFormat, langArgs)
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

// hOCR ppageno starts at zero. ALTO PHYSICAL_IMG_NR follows the one-based page id.
func setOCRPhysicalPage(target string, attrs []xml.Attr, pageNum int) []xml.Attr {
	switch target {
	case "alto":
		return setOCRAttr(attrs, "PHYSICAL_IMG_NR", strconv.Itoa(pageNum))
	case "hocr":
		if pageNum < 1 {
			return attrs
		}
		return rewriteHOCRPageNumber(attrs, pageNum-1)
	default:
		return attrs
	}
}

func rewriteHOCRPageNumber(attrs []xml.Attr, pageNum int) []xml.Attr {
	for i, attr := range attrs {
		if attr.Name.Local != "title" {
			continue
		}
		attrs[i].Value = replaceHOCRPageNumber(attr.Value, pageNum)
	}
	return attrs
}

func replaceHOCRPageNumber(title string, pageNum int) string {
	parts := strings.Split(title, ";")
	changed := false
	for i, part := range parts {
		raw := strings.TrimSpace(part)
		if !strings.HasPrefix(raw, "ppageno") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(raw, "ppageno"))
		if rest == "" || !digitsOnly(rest) {
			continue
		}
		pad := part[:len(part)-len(strings.TrimLeft(part, " \t"))]
		parts[i] = pad + "ppageno " + strconv.Itoa(pageNum)
		changed = true
	}
	if !changed {
		return title
	}
	return strings.Join(parts, ";")
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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
		node.Attrs = setOCRPhysicalPage(m.target, node.Attrs, pageNum)
		inner, err := rewriteDescendantIDs(pageNum, node.Inner)
		if err != nil {
			return fmt.Errorf("ocr: merge: page %d: %w", pageNum, err)
		}
		node.Inner = inner
		m.pages = append(m.pages, node)
		return nil
	}
}

// tesseract starts block, line, and word ids at 1 on every single-page run.
func rewriteDescendantIDs(pageNum int, inner string) (string, error) {
	if strings.TrimSpace(inner) == "" {
		return inner, nil
	}
	dec := xml.NewDecoder(strings.NewReader("<ocrfrag>" + inner + "</ocrfrag>"))
	var toks []fragToken
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 && t.Name.Local == "ocrfrag" {
				depth++
				continue
			}
			depth++
			toks = append(toks, fragToken{kind: 's', start: t.Copy()})
		case xml.EndElement:
			depth--
			if depth == 0 && t.Name.Local == "ocrfrag" {
				continue
			}
			toks = append(toks, fragToken{kind: 'e', end: t})
		case xml.CharData:
			toks = append(toks, fragToken{kind: 'c', data: append([]byte(nil), t...)})
		case xml.Comment:
			toks = append(toks, fragToken{kind: 'm', data: append([]byte(nil), t...)})
		case xml.Directive:
			toks = append(toks, fragToken{kind: 'd', data: append([]byte(nil), t...)})
		}
	}
	ids := map[string]struct{}{}
	for _, tok := range toks {
		if tok.kind != 's' {
			continue
		}
		for _, attr := range tok.start.Attr {
			if isOCRIDAttr(attr.Name.Local) && attr.Value != "" {
				ids[attr.Value] = struct{}{}
			}
		}
	}
	if len(ids) == 0 {
		return inner, nil
	}
	prefix := "p" + strconv.Itoa(pageNum) + "_"
	for i := range toks {
		if toks[i].kind != 's' {
			continue
		}
		for j := range toks[i].start.Attr {
			attr := &toks[i].start.Attr[j]
			if isOCRIDAttr(attr.Name.Local) {
				if _, ok := ids[attr.Value]; ok {
					attr.Value = prefix + attr.Value
				}
				continue
			}
			if isOCRIDRefAttr(attr.Name.Local) {
				attr.Value = rewriteIDRefs(attr.Value, ids, prefix)
			}
		}
	}
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	for _, tok := range toks {
		var err error
		switch tok.kind {
		case 's':
			err = enc.EncodeToken(tok.start)
		case 'e':
			err = enc.EncodeToken(tok.end)
		case 'c':
			err = enc.EncodeToken(xml.CharData(tok.data))
		case 'm':
			err = enc.EncodeToken(xml.Comment(tok.data))
		case 'd':
			err = enc.EncodeToken(xml.Directive(tok.data))
		}
		if err != nil {
			return "", err
		}
	}
	if err := enc.Flush(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

type fragToken struct {
	kind  byte
	start xml.StartElement
	end   xml.EndElement
	data  []byte
}

func isOCRIDAttr(local string) bool {
	return local == "id" || local == "ID"
}

func isOCRIDRefAttr(local string) bool {
	switch local {
	case "IDNEXT", "IDPREV", "STYLEREFS", "TAGREFS", "PROCESSINGREFS":
		return true
	default:
		return false
	}
}

func rewriteIDRefs(value string, ids map[string]struct{}, prefix string) string {
	fields := strings.Fields(value)
	changed := false
	for i, field := range fields {
		if _, ok := ids[field]; ok {
			fields[i] = prefix + field
			changed = true
		}
	}
	if !changed {
		return value
	}
	return strings.Join(fields, " ")
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

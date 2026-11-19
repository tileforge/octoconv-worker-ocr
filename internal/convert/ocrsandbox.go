package convert

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Measured read-only set for tesseract, pdfinfo, pdftoppm, and pdftotext.
// The job directory is the only writable path and is granted per attempt.
var ocrReadOnly = []string{
	"/lib",
	"/usr/local/lib",
	"/usr/local/share/tessdata",
	"/etc/ld.so.cache",
	"/etc/localtime",
	"/usr/share/fonts",
	"/sys/devices/system/cpu/possible",
}

var ocrEngines = []string{"tesseract", "pdftoppm", "pdfinfo", "pdftotext"}

const (
	ocrSandboxDisabledEnv   = "OCR_SANDBOX_DISABLED"
	ocrPreflightOutsideBase = "/var/tmp"
	ocrPreflightText        = "OCTOCONV SANDBOX PREFLIGHT"
	ocrPreflightWord        = "OCTOCONV"
)

func (c OCRConverter) WithSandbox(helper []string) OCRConverter {
	c.sandbox = newEngineSandbox(helper)
	return c
}

func (c OCRConverter) jobExec(workDir string) engineExec {
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
		workDir = resolved
	}
	return c.sandbox.forJobRW(workDir, nil, ocrReadOnly, ocrEngines).withEnv(engineEnv(workDir))
}

// OCRProof is what OCRPreflight observed inside the job directory.
type OCRProof struct {
	LayerBytes int
}

func OCRPreflight(ctx context.Context, c OCRConverter) (OCRProof, error) {
	if !c.sandbox.confines() {
		return OCRProof{}, nil
	}
	w, cleanup, err := newPreflightWorkspaceOutside("ocr", ocrPreflightOutsideBase)
	if err != nil {
		return OCRProof{}, err
	}
	defer cleanup()
	grants := append(append([]string{}, ocrReadOnly...), ocrEnginePaths()...)
	if err := w.requireOutsideGrants(grants); err != nil {
		return OCRProof{}, err
	}

	pdf := ocrPreflightPDF(ocrPreflightText)
	inside, outside, err := w.place("in.pdf", pdf)
	if err != nil {
		return OCRProof{}, err
	}

	layerOut := filepath.Join(w.jobDir, "layer.txt")
	if err := c.Convert(ctx, inside, layerOut, nil); err != nil {
		return OCRProof{}, fmt.Errorf("text-layer route: %w", err)
	}
	layer, err := os.ReadFile(layerOut)
	if err != nil {
		return OCRProof{}, fmt.Errorf("text-layer route: %w", err)
	}
	if !bytes.Contains(layer, []byte(ocrPreflightText)) {
		return OCRProof{}, fmt.Errorf("text-layer route: output does not contain the fixture text")
	}

	renderOut := filepath.Join(w.jobDir, "render.hocr")
	if err := c.Convert(ctx, inside, renderOut, nil); err != nil {
		return OCRProof{}, fmt.Errorf("render route: %w", err)
	}
	if err := requireOutput(renderOut, []byte(ocrPreflightWord)); err != nil {
		return OCRProof{}, fmt.Errorf("render route: %w", err)
	}

	pageOutBase := filepath.Join(w.jobDir, "page")
	if _, err := c.exec(ctx, w.jobDir, "pdftoppm", ocrRasterizePageArgs(inside, pageOutBase, 1, ocrRasterizeDPI)...); err != nil {
		return OCRProof{}, fmt.Errorf("image route: render page: %w", err)
	}
	imageOut := filepath.Join(w.jobDir, "image.txt")
	if err := c.Convert(ctx, pageOutBase+".png", imageOut, nil); err != nil {
		return OCRProof{}, fmt.Errorf("image route: %w", err)
	}
	if err := requireOutput(imageOut, []byte(ocrPreflightWord)); err != nil {
		return OCRProof{}, fmt.Errorf("image route: %w", err)
	}

	negOut := filepath.Join(w.jobDir, "neg.txt")
	convErr := c.Convert(ctx, outside, negOut, nil)
	if err := ocrDeniedOutside(convErr, negOut, []byte(ocrPreflightWord)); err != nil {
		return OCRProof{}, fmt.Errorf("read a file outside the job directory: %w", err)
	}
	return OCRProof{LayerBytes: len(layer)}, nil
}

func ocrPreflightPDF(text string) []byte {
	const width, height = 1000, 200
	content := []byte(fmt.Sprintf("BT\n/F1 40 Tf\n20 100 Td\n(%s) Tj\nET\n", text))
	contentObj := append(
		[]byte(fmt.Sprintf("<< /Length %d >>\nstream\n", len(content))),
		append(content, []byte("\nendstream")...)...,
	)
	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		[]byte(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>", width, height)),
		contentObj,
		[]byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, body := range objects {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n", i+1)
		buf.Write(body)
		buf.WriteString("\nendobj\n")
	}
	xrefAt := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefAt)
	return buf.Bytes()
}

func ocrDeniedOutside(convErr error, outPath string, marker []byte) error {
	if convErr == nil {
		return fmt.Errorf("engine accepted the outside file")
	}
	if errors.Is(convErr, ErrEngineSandboxUnavailable) {
		return fmt.Errorf("sandbox setup failed: %w", convErr)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil
	}
	if bytes.Contains(data, marker) {
		return fmt.Errorf("engine wrote the outside file")
	}
	return nil
}

func ocrEnginePaths() []string {
	var paths []string
	for _, name := range ocrEngines {
		if p, err := exec.LookPath(name); err == nil {
			paths = append(paths, p)
		}
	}
	return paths
}

package onlinebulletins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const PopplerVersion = "25.03.0"

func ParsePDF(path string) (GeometryDocument, error) {
	return ParsePDFContext(context.Background(), path)
}

func ParsePDFContext(parent context.Context, path string) (GeometryDocument, error) {
	return parsePDFContext(parent, path, nil, false)
}

// Recognition keeps character/border evidence without invoking semantic parsers.
func ParseRecognitionPDF(parent context.Context, path string) (GeometryDocument, error) {
	return parsePDFContext(parent, path, nil, true)
}

func parsePDFContext(parent context.Context, path string, consumeImages func(GeometryDocument, map[string]string) error, recognition bool) (GeometryDocument, error) {
	ctx, stop := context.WithTimeout(parent, 120*time.Second)
	defer stop()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	source, err := os.Open(path)
	if err != nil {
		return GeometryDocument{}, ErrUnsupportedSource
	}
	defer source.Close()
	stat, err := source.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() < 5 || stat.Size() > 20<<20 {
		return GeometryDocument{}, ErrGeometryLimits
	}
	var header [5]byte
	if _, err := io.ReadFull(source, header[:]); err != nil || string(header[:]) != "%PDF-" {
		return GeometryDocument{}, ErrUnsupportedSource
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return GeometryDocument{}, ErrUnsupportedSource
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, io.LimitReader(source, (20<<20)+1)); err != nil {
		return GeometryDocument{}, ErrUnsupportedSource
	}
	checksum := hex.EncodeToString(digest.Sum(nil))
	executable, err := exec.LookPath("pdftohtml")
	if err != nil {
		return GeometryDocument{}, ErrUnsupportedSource
	}
	directory, err := os.MkdirTemp("", "hhc-bulletin-geometry-*")
	if err != nil {
		return GeometryDocument{}, err
	}
	defer os.RemoveAll(directory)
	monitorDone := make(chan struct{})
	defer close(monitorDone)
	go func() {
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-monitorDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := boundedOutputDirectory(directory); err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	run := func(binary string, args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, binary, args...)
		command.WaitDelay = time.Second
		command.Dir = directory
		command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
		stdout, stderr := &limitedProcessOutput{}, &limitedProcessOutput{}
		command.Stdout, command.Stderr = stdout, stderr
		if command.Run() != nil || stdout.overflow || stderr.overflow {
			if stdout.overflow || stderr.overflow {
				return nil, ErrGeometryLimits
			}
			if cause := context.Cause(ctx); cause != nil {
				return nil, cause
			}
			return nil, ErrUnsupportedSource
		}
		return append(stdout.Bytes(), stderr.Bytes()...), nil
	}
	version, err := run(executable, "-v")
	if err != nil || !bytes.Contains(version, []byte("pdftohtml version "+PopplerVersion+"\n")) {
		return GeometryDocument{}, ErrUnsupportedSource
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return GeometryDocument{}, ErrUnsupportedSource
	}
	infoBinary := filepath.Join(filepath.Dir(executable), "pdfinfo")
	info, err := run(infoBinary, absolute)
	if err != nil {
		return GeometryDocument{}, err
	}
	count, err := supportedPDFInfo(info)
	if err != nil {
		return GeometryDocument{}, err
	}
	info, err = run(infoBinary, "-f", "1", "-l", strconv.Itoa(count), "-box", absolute)
	if err != nil {
		return GeometryDocument{}, err
	}
	pages, err := sourcePDFPages(info, count)
	if err != nil {
		return GeometryDocument{}, err
	}
	var variants [2]GeometryDocument
	for index := range variants {
		output := filepath.Join(directory, fmt.Sprintf("geometry-%d.xml", index))
		args := []string{"-xml", "-zoom", "1", "-noroundcoord", "-nomerge", "-fontfullname"}
		if index == 1 {
			args = append(args, "-hidden")
		}
		args = append(args, absolute, output)
		if _, err := run(executable, args...); err != nil {
			return GeometryDocument{}, err
		}
		if err := boundedOutputDirectory(directory); err != nil {
			return GeometryDocument{}, err
		}
		file, err := os.Open(output)
		if err != nil {
			return GeometryDocument{}, ErrInvalidGeometry
		}
		variants[index], err = parseGeometryXML(file, pages)
		closeErr := file.Close()
		if err != nil {
			return GeometryDocument{}, err
		}
		if closeErr != nil {
			return GeometryDocument{}, closeErr
		}
	}
	// -hidden is diagnostic only: invisible text must never enter editable/AI content.
	for index, page := range variants[0].Pages {
		other := variants[1].Pages[index]
		if len(page.Texts) != len(other.Texts) {
			return GeometryDocument{}, ErrUnsupportedSource
		}
		for n, text := range page.Texts {
			if text != other.Texts[n] {
				return GeometryDocument{}, ErrUnsupportedSource
			}
		}
	}
	result := variants[0]
	fontNames, err := run("/usr/bin/python3", "/opt/bulletin-fonts/fonts.py", absolute)
	if err != nil {
		return GeometryDocument{}, err
	}
	if err := restoreEmbeddedFontNames(&result, fontNames); err != nil {
		return GeometryDocument{}, err
	}
	result.SourceSHA256, result.PopplerVersion = checksum, PopplerVersion
	if recognition {
		if err := loadRecognitionGlyphs(ctx, absolute, &result); err != nil {
			return GeometryDocument{}, err
		}
	}
	if len(result.Pages) > 1 {
		// Cairo provides native decoration bounds absent from pdftohtml XML. Only
		// closed numeric frame evidence survives; SVG/font/image bytes do not.
		cairo := filepath.Join(filepath.Dir(executable), "pdftocairo")
		version, err = run(cairo, "-v")
		if err != nil || !bytes.Contains(version, []byte("pdftocairo version "+PopplerVersion+"\n")) {
			return GeometryDocument{}, ErrUnsupportedSource
		}
		vectorPath := filepath.Join(directory, "page-frames.svg")
		for index := 1; index < len(result.Pages); index++ {
			page := &result.Pages[index]
			number := strconv.Itoa(page.Number)
			if _, err := run(cairo, "-f", number, "-l", number, "-svg", absolute, vectorPath); err != nil {
				return GeometryDocument{}, err
			}
			if err := boundedOutputDirectory(directory); err != nil {
				return GeometryDocument{}, err
			}
			vectorFile, err := os.Open(vectorPath)
			if err != nil {
				return GeometryDocument{}, ErrInvalidGeometry
			}
			if index == len(result.Pages)-1 {
				page.Frames, err = parseBackFrameSVG(vectorFile, page.Width, page.Height)
			} else {
				page.Frames, err = parseBodyFrameSVG(vectorFile, page.Width, page.Height)
			}
			closeErr := vectorFile.Close()
			// Optional border evidence must not reintroduce layout-specific parsing failures.
			if recognition && errors.Is(err, ErrUnsupportedSource) {
				page.Frames, err = nil, nil
			}
			if err != nil {
				return GeometryDocument{}, err
			}
			if closeErr != nil {
				return GeometryDocument{}, closeErr
			}
		}
	}
	imageFiles := map[string]string{}
	for index := range result.Pages {
		for n := range result.Pages[index].Images {
			image := &result.Pages[index].Images[n]
			name := image.source
			if err := imageEvidence(directory, image); err != nil {
				return GeometryDocument{}, err
			}
			if consumeImages != nil {
				imageFiles[image.SHA256] = filepath.Join(directory, filepath.Base(name))
			}
		}
	}
	if err := context.Cause(ctx); err != nil {
		return GeometryDocument{}, err
	}
	if consumeImages != nil {
		if err := consumeImages(result, imageFiles); err != nil {
			return GeometryDocument{}, err
		}
	}
	return result, nil
}

type limitedProcessOutput struct {
	bytes.Buffer
	overflow bool
	limit    int
}

func (w *limitedProcessOutput) Write(value []byte) (int, error) {
	limit := w.limit
	if limit == 0 {
		limit = 64 << 10
	}
	if len(value)+w.Len() > limit {
		w.overflow = true
		return 0, ErrGeometryLimits
	}
	return w.Buffer.Write(value)
}

func boundedOutputDirectory(directory string) error {
	var size int64
	return filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrGeometryLimits
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ErrGeometryLimits
		}
		size += info.Size()
		if size > 100<<20 {
			return ErrGeometryLimits
		}
		return nil
	})
}

var pagesPattern = regexp.MustCompile(`(?m)^Pages:\s+(\d+)\s*$`)
var pageSizePattern = regexp.MustCompile(`(?m)^Page\s+(\d+) size:\s+([0-9.]+) x ([0-9.]+) pts`)

func supportedPDFInfo(info []byte) (int, error) {
	for _, field := range []string{"Encrypted", "JavaScript", "Form"} {
		pattern := regexp.MustCompile(`(?m)^` + field + `:\s+(\S+)`)
		value := pattern.FindSubmatch(info)
		expected := "no"
		if field == "Form" {
			expected = "none"
		}
		if len(value) != 2 || string(value[1]) != expected {
			return 0, ErrUnsupportedSource
		}
	}
	match := pagesPattern.FindSubmatch(info)
	if len(match) != 2 {
		return 0, ErrInvalidGeometry
	}
	count, err := strconv.Atoi(string(match[1]))
	if err != nil || count < 1 || count > 40 {
		return 0, ErrGeometryLimits
	}
	return count, nil
}

func sourcePDFPages(info []byte, count int) ([]Page, error) {
	matches := pageSizePattern.FindAllSubmatch(info, -1)
	if len(matches) != count {
		return nil, ErrInvalidGeometry
	}
	pages := make([]Page, count)
	for index, match := range matches {
		number, _ := strconv.Atoi(string(match[1]))
		width, errW := strconv.ParseFloat(string(match[2]), 64)
		height, errH := strconv.ParseFloat(string(match[3]), 64)
		if number != index+1 || errW != nil || errH != nil || !finitePositive(width) || !finitePositive(height) || width > 3000 || height > 3000 {
			return nil, ErrInvalidGeometry
		}
		pages[index] = Page{ID: fmt.Sprintf("page-%d", number), Width: width, Height: height}
	}
	return pages, nil
}

func imageEvidence(directory string, evidence *GeometryImage) error {
	name := evidence.source
	if filepath.IsAbs(name) {
		name = strings.TrimPrefix(name, directory+string(os.PathSeparator))
	}
	if filepath.Base(name) != name || name == "." || name == "" {
		return ErrInvalidGeometry
	}
	file, err := os.Open(filepath.Join(directory, name))
	if err != nil {
		return ErrInvalidGeometry
	}
	defer file.Close()
	configuration, _, err := image.DecodeConfig(io.LimitReader(file, 100<<20))
	if err != nil || configuration.Width < 1 || configuration.Height < 1 || int64(configuration.Width)*int64(configuration.Height) > 100000000 {
		return ErrInvalidGeometry
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ErrInvalidGeometry
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, io.LimitReader(file, (100<<20)+1)); err != nil {
		return ErrInvalidGeometry
	}
	evidence.Width, evidence.Height = configuration.Width, configuration.Height
	evidence.SHA256 = hex.EncodeToString(digest.Sum(nil))
	evidence.source = ""
	return nil
}

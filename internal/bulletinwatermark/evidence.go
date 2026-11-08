package bulletinwatermark

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"path/filepath"
)

const (
	MaxEvidenceFiles       = 5
	MaxEvidenceBytes       = 10 << 20
	MaxStoredEvidenceBytes = MaxEvidenceBytes + (16 << 10)
	MaxImagePixels         = 20_000_000
	MaxPages               = 60
	EvidencePNG            = "png"
	EvidenceJPEG           = "jpeg"
	EvidencePDF            = "pdf"
)

type EvidenceFile struct {
	Name string
	Kind string
	Data []byte
}

func evidenceKind(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return EvidencePNG
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return EvidenceJPEG
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return EvidencePDF
	default:
		return ""
	}
}

func PackEvidence(input []EvidenceFile) ([]byte, error) {
	if len(input) == 0 || len(input) > MaxEvidenceFiles {
		return nil, ErrInvalidInvestigation
	}
	seen := map[[sha256.Size]byte]bool{}
	files := make([]EvidenceFile, 0, len(input))
	total := 0
	for _, file := range input {
		kind := evidenceKind(file.Data)
		if kind == "" || len(file.Data) == 0 {
			return nil, ErrInvalidInvestigation
		}
		hash := sha256.Sum256(file.Data)
		if !seen[hash] {
			total += len(file.Data)
			if total > MaxEvidenceBytes {
				return nil, ErrInvalidInvestigation
			}
			seen[hash] = true
			files = append(files, EvidenceFile{Kind: kind, Data: file.Data})
		}
	}
	var output bytes.Buffer
	w := zip.NewWriter(&output)
	for i, file := range files {
		ext := file.Kind
		if ext == EvidenceJPEG {
			ext = "jpg"
		}
		header := &zip.FileHeader{Name: fmt.Sprintf("%02d.%s", i+1, ext), Method: zip.Store}
		entry, err := w.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err = entry.Write(file.Data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if output.Len() > MaxStoredEvidenceBytes {
		return nil, ErrInvalidInvestigation
	}
	return output.Bytes(), nil
}

func UnpackEvidence(data []byte) ([]EvidenceFile, error) {
	if len(data) > MaxStoredEvidenceBytes {
		return nil, ErrInvalidInvestigation
	}
	if kind := evidenceKind(data); kind == EvidencePNG || kind == EvidenceJPEG {
		return []EvidenceFile{{Kind: kind, Data: data}}, nil
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(r.File) == 0 || len(r.File) > MaxEvidenceFiles {
		return nil, ErrInvalidInvestigation
	}
	files := make([]EvidenceFile, 0, len(r.File))
	total := 0
	for i, entry := range r.File {
		ext := filepath.Ext(entry.Name)
		expected := fmt.Sprintf("%02d", i+1)
		if filepath.Base(entry.Name[:len(entry.Name)-len(ext)]) != expected || (ext != ".png" && ext != ".jpg" && ext != ".pdf") || entry.UncompressedSize64 > MaxEvidenceBytes {
			return nil, ErrInvalidInvestigation
		}
		reader, err := entry.Open()
		if err != nil {
			return nil, ErrInvalidInvestigation
		}
		body, readErr := io.ReadAll(io.LimitReader(reader, int64(MaxEvidenceBytes-total+1)))
		closeErr := reader.Close()
		total += len(body)
		kind := evidenceKind(body)
		if readErr != nil || closeErr != nil || total > MaxEvidenceBytes || kind == "" {
			return nil, ErrInvalidInvestigation
		}
		files = append(files, EvidenceFile{Kind: kind, Data: body})
	}
	return files, nil
}

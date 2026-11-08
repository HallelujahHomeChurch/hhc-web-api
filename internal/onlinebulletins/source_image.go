package onlinebulletins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"io"
	"os"
)

// ParsePDFWithSourceImages prepares only privacy-filtered image parts while
// native files remain in the worker's private temporary directory.
func ParsePDFWithSourceImages(ctx context.Context, path string, prepare func(SourcePart, []byte) error) (GeometryDocument, error) {
	return parsePDFWithSourceImages(ctx, path, prepare, false)
}

func ParseRecognitionPDFWithSourceImages(ctx context.Context, path string, prepare func(SourcePart, []byte) error) (GeometryDocument, error) {
	return parsePDFWithSourceImages(ctx, path, prepare, true)
}

func parsePDFWithSourceImages(ctx context.Context, path string, prepare func(SourcePart, []byte) error, recognition bool) (GeometryDocument, error) {
	return parsePDFContext(ctx, path, func(source GeometryDocument, files map[string]string) error {
		allowed, _, err := filterTemplateV1Geometry(source)
		if err != nil {
			return err
		}
		parts, err := CatalogParts(allowed)
		if err != nil {
			return err
		}
		count, total := 0, 0
		for _, part := range parts {
			if part.Kind != "image" || count >= MaxIllustrations {
				continue
			}
			file, err := os.Open(files[part.imageSHA])
			if err != nil {
				return ErrSourceRepair
			}
			data, imageErr := sourceImagePNG(file, part.imageSHA)
			closeErr := file.Close()
			if closeErr != nil {
				return closeErr
			}
			// Out-of-policy images stay visible as unavailable source parts. They
			// must not make an otherwise valid text conversion impossible.
			if imageErr != nil || total+len(data) > MaxIllustrationTotalBytes {
				continue
			}
			if err := prepare(part, data); err != nil {
				return err
			}
			count++
			total += len(data)
		}
		return nil
	}, recognition)
}

func sourceImagePNG(source io.Reader, expected string) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(source, (20<<20)+1))
	if err != nil || len(raw) > 20<<20 {
		return nil, ErrGeometryLimits
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != expected {
		return nil, ErrSourceRepair
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 4_000_000 {
		return nil, ErrGeometryLimits
	}
	pixels, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, ErrSourceRepair
	}
	// Re-encode pixels, rather than copying EXIF/comments or an uploaded SVG.
	var output bytes.Buffer
	if err := png.Encode(&output, pixels); err != nil || output.Len() > MaxIllustrationBytes {
		return nil, ErrGeometryLimits
	}
	return output.Bytes(), nil
}

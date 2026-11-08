package onlinebulletins

import (
	"encoding/xml"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidGeometry   = errors.New("invalid_pdf_geometry")
	ErrGeometryLimits    = errors.New("pdf_geometry_limit")
	ErrUnsupportedSource = errors.New("unsupported_pdf_source")
)

type GeometryDocument struct {
	SourceSHA256   string
	PopplerVersion string
	Pages          []GeometryPage
}
type GeometryPage struct {
	Number            int
	Width, Height     float64
	Texts             []GeometryText
	Images            []GeometryImage
	Frames            []GeometryFrame
	Glyphs            []GeometryGlyph `json:",omitempty"`
	backContentBottom float64         // Set only by the source-table exclusion filter.
}

type GeometryGlyph struct {
	Text string
	Box  Box
}
type GeometryText struct {
	Text, FontFamily string
	FontSize         float64
	Box              Box
}
type GeometryImage struct {
	Box           Box
	Width, Height int
	SHA256        string
	source        string
}

type xmlFont struct {
	ID     string  `xml:"id,attr"`
	Size   float64 `xml:"size,attr"`
	Family string  `xml:"family,attr"`
}
type xmlText struct {
	Top    float64 `xml:"top,attr"`
	Left   float64 `xml:"left,attr"`
	Width  float64 `xml:"width,attr"`
	Height float64 `xml:"height,attr"`
	Font   string  `xml:"font,attr"`
	Text   string
}

// Poppler uses b/i/a as inert inline text wrappers. Never retain href or markup.
func (t *xmlText) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	seen := map[string]bool{}
	for _, attribute := range start.Attr {
		if seen[attribute.Name.Local] {
			return ErrInvalidGeometry
		}
		seen[attribute.Name.Local] = true
		if attribute.Name.Local == "font" {
			t.Font = attribute.Value
			continue
		}
		value, err := strconv.ParseFloat(attribute.Value, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return ErrInvalidGeometry
		}
		switch attribute.Name.Local {
		case "top":
			t.Top = value
		case "left":
			t.Left = value
		case "width":
			t.Width = value
		case "height":
			t.Height = value
		default:
			return ErrInvalidGeometry
		}
	}
	for _, name := range []string{"top", "left", "width", "height", "font"} {
		if !seen[name] {
			return ErrInvalidGeometry
		}
	}
	var text strings.Builder
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return ErrInvalidGeometry
		}
		switch value := token.(type) {
		case xml.CharData:
			if text.Len()+len(value) > 64<<10 {
				return ErrGeometryLimits
			}
			text.Write(value)
		case xml.StartElement:
			if value.Name.Local != "b" && value.Name.Local != "i" && value.Name.Local != "a" {
				return ErrInvalidGeometry
			}
			depth++
			if depth > 8 {
				return ErrGeometryLimits
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			return ErrInvalidGeometry
		}
	}
	t.Text = text.String()
	return nil
}

type xmlImage struct {
	Top    float64 `xml:"top,attr"`
	Left   float64 `xml:"left,attr"`
	Width  float64 `xml:"width,attr"`
	Height float64 `xml:"height,attr"`
	Source string  `xml:"src,attr"`
}

func parseGeometryXML(reader io.Reader, sourcePages []Page) (GeometryDocument, error) {
	result := GeometryDocument{Pages: []GeometryPage{}}
	if len(sourcePages) < 1 || len(sourcePages) > 40 {
		return result, ErrGeometryLimits
	}
	decoder := xml.NewDecoder(io.LimitReader(reader, (100<<20)+1))
	fonts := map[string]xmlFont{}
	var page *GeometryPage
	var root, closed bool
	textCount, imageCount := 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return GeometryDocument{}, ErrInvalidGeometry
		}
		switch value := token.(type) {
		case xml.Directive:
			if string(value) != `DOCTYPE pdf2xml SYSTEM "pdf2xml.dtd"` {
				return GeometryDocument{}, ErrInvalidGeometry
			}
		case xml.StartElement:
			if closed {
				return GeometryDocument{}, ErrInvalidGeometry
			}
			switch value.Name.Local {
			case "pdf2xml":
				if root {
					return GeometryDocument{}, ErrInvalidGeometry
				}
				root = true
			case "page":
				if !root || page != nil || len(result.Pages) >= len(sourcePages) {
					return GeometryDocument{}, ErrInvalidGeometry
				}
				attributes := map[string]string{}
				for _, attribute := range value.Attr {
					attributes[attribute.Name.Local] = attribute.Value
				}
				number, err := strconv.Atoi(attributes["number"])
				if err != nil || number != len(result.Pages)+1 {
					return GeometryDocument{}, ErrInvalidGeometry
				}
				source := sourcePages[number-1]
				width, errW := strconv.ParseFloat(attributes["width"], 64)
				height, errH := strconv.ParseFloat(attributes["height"], 64)
				if errW != nil || errH != nil || !finitePositive(source.Width) || !finitePositive(source.Height) || !finitePositive(width) || !finitePositive(height) || math.Abs(width-source.Width) >= 1 || math.Abs(height-source.Height) >= 1 {
					return GeometryDocument{}, ErrInvalidGeometry
				}
				result.Pages = append(result.Pages, GeometryPage{Number: number, Width: source.Width, Height: source.Height, Texts: []GeometryText{}, Images: []GeometryImage{}})
				page = &result.Pages[len(result.Pages)-1]
			case "fontspec":
				if page == nil {
					return GeometryDocument{}, ErrInvalidGeometry
				}
				var font xmlFont
				if decoder.DecodeElement(&font, &value) != nil || font.ID == "" || font.Family == "" || !finitePositive(font.Size) || font.Size > 192 {
					return GeometryDocument{}, ErrInvalidGeometry
				}
				if previous, exists := fonts[font.ID]; exists && previous != font {
					return GeometryDocument{}, ErrInvalidGeometry
				}
				fonts[font.ID] = font
				if len(fonts) > 4096 {
					return GeometryDocument{}, ErrGeometryLimits
				}
			case "text":
				if page == nil {
					return GeometryDocument{}, ErrInvalidGeometry
				}
				var text xmlText
				if err := decoder.DecodeElement(&text, &value); err != nil {
					return GeometryDocument{}, ErrInvalidGeometry
				}
				if strings.TrimSpace(text.Text) == "" {
					continue
				}
				font, exists := fonts[text.Font]
				box := Box{X: text.Left / page.Width, Y: text.Top / page.Height, Width: text.Width / page.Width, Height: text.Height / page.Height}
				if !exists || !utf8.ValidString(text.Text) || strings.ContainsRune(text.Text, '\uFFFD') {
					return GeometryDocument{}, ErrInvalidGeometry
				}
				textCount++
				if textCount > 250000 {
					return GeometryDocument{}, ErrGeometryLimits
				}
				page.Texts = append(page.Texts, GeometryText{Text: text.Text, FontFamily: font.Family, FontSize: font.Size, Box: box})
			case "image":
				if page == nil {
					return GeometryDocument{}, ErrInvalidGeometry
				}
				var image xmlImage
				if decoder.DecodeElement(&image, &value) != nil {
					return GeometryDocument{}, ErrInvalidGeometry
				}
				box := Box{X: image.Left / page.Width, Y: image.Top / page.Height, Width: image.Width / page.Width, Height: image.Height / page.Height}
				if !geometryBox(box) || image.Source == "" {
					return GeometryDocument{}, ErrInvalidGeometry
				}
				imageCount++
				if imageCount > 200 {
					return GeometryDocument{}, ErrGeometryLimits
				}
				page.Images = append(page.Images, GeometryImage{Box: box, source: image.Source})
			case "outline":
				if !root || page != nil || decoder.Skip() != nil {
					return GeometryDocument{}, ErrInvalidGeometry
				}
			default:
				return GeometryDocument{}, ErrInvalidGeometry
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "page":
				if page == nil {
					return GeometryDocument{}, ErrInvalidGeometry
				}
				retained := make([]GeometryText, 0, len(page.Texts))
				for _, text := range page.Texts {
					if !geometryBox(text.Box) {
						if len(sourcePages) >= 4 && page.Number == len(sourcePages) && excludedBackOverflow(*page, text.Box) {
							continue
						}
						return GeometryDocument{}, ErrInvalidGeometry
					}
					retained = append(retained, text)
				}
				page.Texts = retained
				page = nil
			case "pdf2xml":
				closed = true
			default:
				return GeometryDocument{}, ErrInvalidGeometry
			}
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				return GeometryDocument{}, ErrInvalidGeometry
			}
		}
	}
	if !root || !closed || page != nil || len(result.Pages) != len(sourcePages) {
		return GeometryDocument{}, ErrInvalidGeometry
	}
	for index := range result.Pages {
		orderGeometryText(result.Pages[index].Texts, index == len(result.Pages)-2)
	}
	return result, nil
}

// Some exports give offering-list text a width beyond the paper edge. Only
// discard horizontal overflow wholly inside the verified, excluded back band.
func excludedBackOverflow(page GeometryPage, box Box) bool {
	if !((math.Abs(page.Width-templateV1Width) <= .02 && math.Abs(page.Height-templateV1Height) <= .02) || (page.Width == 612 && page.Height == 792)) ||
		!finitePositive(box.Width) || box.X < 0 || box.X >= 1 || box.Y*page.Height < 474 ||
		!geometryBox(Box{X: 0, Y: box.Y, Width: 1, Height: box.Height}) {
		return false
	}
	for _, text := range page.Texts {
		top := text.Box.Y * page.Height
		if geometryBox(text.Box) && top >= backAnchorMinimum(page) && top <= 474 && strings.Contains(strings.Join(strings.Fields(text.Text), ""), "主日敬拜服事表") {
			return true
		}
	}
	return false
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
func geometryBox(box Box) bool {
	return finitePositive(box.Width) && finitePositive(box.Height) && !math.IsNaN(box.X) && !math.IsInf(box.X, 0) && !math.IsNaN(box.Y) && !math.IsInf(box.Y, 0) && box.X >= -.002 && box.Y >= -.002 && box.X+box.Width <= 1.002 && box.Y+box.Height <= 1.002
}
func orderGeometryText(texts []GeometryText, columns bool) {
	sort.SliceStable(texts, func(i, j int) bool {
		a, b := texts[i], texts[j]
		if columns && (a.Box.X >= .5) != (b.Box.X >= .5) {
			return a.Box.X < .5
		}
		if math.Round(a.Box.Y/.001) != math.Round(b.Box.Y/.001) {
			return a.Box.Y < b.Box.Y
		}
		return a.Box.X < b.Box.X
	})
}

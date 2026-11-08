package onlinebulletins

import (
	"encoding/xml"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type GeometryFrame struct {
	Element string
	Box     Box
}

var svgPathToken = regexp.MustCompile(`[MLCZ]|[-+]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][-+]?[0-9]+)?`)

func parseBodyFrameSVG(input io.Reader, width, height float64) ([]GeometryFrame, error) {
	return parseFrameSVG(input, width, height, true)
}

// Only the three reviewed back-frame families yield numeric bounds. SVG markup,
// glyph paths, fonts, images and source text never enter the candidate or DOM.
func parseBackFrameSVG(input io.Reader, width, height float64) ([]GeometryFrame, error) {
	return parseFrameSVG(input, width, height, false)
}

func parseFrameSVG(input io.Reader, width, height float64, body bool) ([]GeometryFrame, error) {
	limited := &io.LimitedReader{R: input, N: 32<<20 + 1}
	decoder := xml.NewDecoder(limited)
	depth, definitions, count := 0, 0, 0
	frames := map[string]Box{}
	var matrix []float64
	var prayerBoxes []Box
	var bodyEdges []Box
	rootSeen := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil || limited.N <= 0 {
			return nil, ErrInvalidGeometry
		}
		count++
		if count > 1000000 {
			return nil, ErrGeometryLimits
		}
		switch value := token.(type) {
		case xml.Directive:
			return nil, ErrInvalidGeometry
		case xml.StartElement:
			depth++
			if depth > 64 {
				return nil, ErrGeometryLimits
			}
			attributes := map[string]string{}
			for _, attribute := range value.Attr {
				if _, duplicate := attributes[attribute.Name.Local]; duplicate {
					return nil, ErrInvalidGeometry
				}
				attributes[attribute.Name.Local] = attribute.Value
			}
			if depth == 1 {
				if rootSeen || value.Name.Local != "svg" || attributes["viewBox"] != "0 0 "+strconv.FormatFloat(width, 'f', -1, 64)+" "+strconv.FormatFloat(height, 'f', -1, 64) {
					return nil, ErrInvalidGeometry
				}
				rootSeen = true
			}
			if value.Name.Local == "defs" {
				definitions++
			}
			if definitions == 0 && value.Name.Local == "g" && attributes["transform"] != "" {
				return nil, ErrUnsupportedSource
			}
			if definitions != 0 || value.Name.Local != "path" || len(attributes["d"]) > 4096 {
				continue
			}
			if body {
				if attributes["fill"] == "rgb(0%, 0%, 0%)" && attributes["fill-opacity"] == "1" && attributes["transform"] == "" {
					for _, path := range strings.Split(attributes["d"], "Z") {
						if box, ok := svgRectangleBounds(path + " Z"); ok && ((box.Width <= 2 && box.Height > 8 && box.Height < 45) || (box.Height <= 2 && box.Width > 15)) {
							bodyEdges = append(bodyEdges, box)
							if len(bodyEdges) > 512 {
								return nil, ErrGeometryLimits
							}
						}
					}
				}
				continue
			}
			stroke := attributes["stroke-width"]
			whiteOrEmpty := attributes["fill"] == "none" || attributes["fill"] == "rgb(100%, 100%, 100%)" && attributes["fill-opacity"] == "1"
			outlined := whiteOrEmpty && attributes["stroke"] == "rgb(0%, 0%, 0%)" && (stroke == "0.75" || stroke == "1")
			scaledOutline := whiteOrEmpty && attributes["stroke"] == "rgb(0%, 0%, 0%)" && stroke == "0.96"
			prayer := attributes["fill"] == "rgb(50.19989%, 50.19989%, 50.19989%)" && attributes["fill-opacity"] == "0.50196" && attributes["transform"] == ""
			if !outlined && !prayer && !scaledOutline {
				continue
			}
			box, valid := svgPathBounds(attributes["d"])
			if !valid {
				continue
			}
			commands := strings.Map(func(point rune) rune {
				if strings.ContainsRune("MLCZ", point) {
					return point
				}
				return -1
			}, attributes["d"])
			if scaledOutline {
				transform := svgMatrix(attributes["transform"])
				if transform == nil || transform[0] != .75 || transform[1] != 0 || transform[2] != 0 || transform[3] != .75 {
					continue
				}
				box = Box{X: box.X*.75 + transform[4], Y: box.Y*.75 + transform[5], Width: box.Width * .75, Height: box.Height * .75}
				element := ""
				if commands == "MCLLCCLCLLCCZM" && box.X/width >= .12 && box.X/width <= .20 && box.Width/width >= .70 && box.Width/width <= .85 && box.Y >= 0 && box.Y < height*.1 && box.Height >= 100 && box.Height <= 240 {
					element = "summaryFrame"
				} else if _, rectangle := svgRectangleBounds(attributes["d"]); rectangle && box.X/width >= .04 && box.X/width <= .09 && box.Width/width >= .80 && box.Width/width <= .93 && box.Y > height*.38 && box.Height >= 16 && box.Height <= 180 {
					element = "prayersFrame"
				}
				if element == "" || box.Y+box.Height > templateV1BackAnchorMax {
					continue
				}
				if _, duplicate := frames[element]; duplicate {
					return nil, ErrInvalidGeometry
				}
				frames[element] = box
				continue
			}
			if prayer {
				if commands == "MLLLZM" && box.Width > 550 && box.Width < 565 && box.X > 17 && box.X < 20 {
					prayerBoxes = append(prayerBoxes, box)
					if len(prayerBoxes) > 4 {
						return nil, ErrGeometryLimits
					}
				}
				continue
			}
			transform := svgPageMatrix(attributes["transform"], height)
			if transform == nil {
				continue
			}
			box.Y = height - box.Y - box.Height
			element := ""
			if commands == "MCLLCCLCLLCCZM" && math.Abs(box.X-75.75) < .02 && math.Abs(box.Width-486.4) < .02 && math.Abs(box.Y-11.25) < .02 && box.Height >= 100 && box.Height <= 240 && stroke == "0.75" {
				element = "summaryFrame"
			} else if commands == "MLLLZM" && math.Abs(box.X-12.75) < .02 && math.Abs(box.Width-560.8) < .02 && box.Y > 175 && box.Height > 50 && attributes["stroke-dasharray"] == "4 3" && stroke == "1" {
				element = "announcementsFrame"
			}
			if element == "" || box.Y+box.Height > templateV1BackAnchorMax {
				continue
			}
			if matrix != nil && !sameSVGMatrix(matrix, transform) {
				return nil, ErrInvalidGeometry
			}
			matrix = transform
			if _, duplicate := frames[element]; duplicate {
				return nil, ErrInvalidGeometry
			}
			frames[element] = box
		case xml.EndElement:
			if value.Name.Local == "defs" {
				definitions--
			}
			depth--
		}
	}
	if !rootSeen || depth != 0 {
		return nil, ErrInvalidGeometry
	}
	if body {
		return bodyFramesFromEdges(bodyEdges, width, height), nil
	}
	if matrix != nil {
		for _, box := range prayerBoxes {
			box.X, box.Y = (box.X-matrix[4])/matrix[0], height+(box.Y-matrix[5])/matrix[0]
			box.Width, box.Height = box.Width/matrix[0], box.Height/matrix[0]
			if math.Abs(box.X-18.75) > .02 || math.Abs(box.Width-559.45) > .02 || box.Y < 300 || box.Height < 30 || box.Height > 140 || box.Y+box.Height > templateV1BackAnchorMax {
				continue
			}
			if _, duplicate := frames["prayersFrame"]; duplicate {
				return nil, ErrInvalidGeometry
			}
			frames["prayersFrame"] = box
		}
	}
	result := []GeometryFrame{}
	for _, element := range []string{"summaryFrame", "announcementsFrame", "prayersFrame"} {
		if box, ok := frames[element]; ok {
			box = Box{X: box.X / width, Y: box.Y / height, Width: box.Width / width, Height: box.Height / height}
			if !validBox(box) {
				return nil, ErrInvalidGeometry
			}
			result = append(result, GeometryFrame{Element: element, Box: box})
		}
	}
	return result, nil
}

func svgRectangleBounds(path string) (Box, bool) {
	box, ok := svgPathBounds(path)
	if !ok {
		return Box{}, false
	}
	tokens := svgPathToken.FindAllString(path, -1)
	if len(tokens) != 13 && len(tokens) != 16 {
		return Box{}, false
	}
	for i, command := range []string{"M", "L", "L", "L"} {
		if tokens[i*3] != command {
			return Box{}, false
		}
	}
	if tokens[12] != "Z" {
		return Box{}, false
	}
	points := [4][2]float64{}
	for i := range points {
		points[i][0], _ = strconv.ParseFloat(tokens[i*3+1], 64)
		points[i][1], _ = strconv.ParseFloat(tokens[i*3+2], 64)
	}
	for i, p := range points {
		next := points[(i+1)%4]
		if (p[0] == next[0]) == (p[1] == next[1]) {
			return Box{}, false
		}
	}
	if len(tokens) == 16 && (tokens[13] != "M" || tokens[14] != tokens[1] || tokens[15] != tokens[2]) {
		return Box{}, false
	}
	return box, true
}

func bodyFramesFromEdges(edges []Box, width, height float64) []GeometryFrame {
	var vertical, horizontal []Box
	for _, edge := range edges {
		if edge.Width <= 2 && edge.Height > 8 && edge.Height < 45 {
			vertical = append(vertical, edge)
		}
		if edge.Height <= 2 && edge.Width > 15 {
			horizontal = append(horizontal, edge)
		}
	}
	result := []GeometryFrame{}
	seen := map[Box]bool{}
	for _, left := range vertical {
		for _, right := range vertical {
			if right.X-left.X < 15 || math.Abs(left.Y-right.Y) > .5 || math.Abs(left.Height-right.Height) > .5 {
				continue
			}
			top, bottom := false, false
			for _, edge := range horizontal {
				if math.Abs(edge.X-left.X-left.Width) > 1 || math.Abs(edge.X+edge.Width-right.X) > 1 {
					continue
				}
				top = top || math.Abs(edge.Y+edge.Height-left.Y) < .5
				bottom = bottom || math.Abs(edge.Y-left.Y-left.Height) < .5
			}
			box := Box{X: left.X / width, Y: left.Y / height, Width: (right.X + right.Width - left.X) / width, Height: left.Height / height}
			if top && bottom && validBox(box) && !seen[box] {
				seen[box] = true
				result = append(result, GeometryFrame{Element: "bodyHeadingFrame", Box: box})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Box.Y != result[j].Box.Y {
			return result[i].Box.Y < result[j].Box.Y
		}
		return result[i].Box.X < result[j].Box.X
	})
	return result
}

// Reviewed Cairo back frames use absolute M/L/C/Z only. Control-point bounds
// equal their outer extrema; no general-purpose SVG interpreter is required.
func svgPathBounds(path string) (Box, bool) {
	tokens := svgPathToken.FindAllStringIndex(path, -1)
	if len(tokens) < 9 || len(tokens) > 256 {
		return Box{}, false
	}
	values := []float64{}
	offset, command, remaining := 0, "", 0
	closed := false
	for _, token := range tokens {
		if strings.Trim(path[offset:token[0]], " \t\r\n,") != "" {
			return Box{}, false
		}
		part := path[token[0]:token[1]]
		offset = token[1]
		if strings.Contains("MLCZ", part) {
			if remaining != 0 {
				return Box{}, false
			}
			command = part
			remaining = map[string]int{"M": 2, "L": 2, "C": 6, "Z": 0}[part]
			closed = closed || part == "Z"
			continue
		}
		if command == "" || remaining == 0 {
			return Box{}, false
		}
		value, err := strconv.ParseFloat(part, 64)
		if err != nil || !finite(value) || math.Abs(value) > 2000 {
			return Box{}, false
		}
		values = append(values, value)
		remaining--
	}
	if !closed || remaining != 0 || strings.TrimSpace(path[offset:]) != "" || len(values) < 8 || len(values)%2 != 0 || !strings.HasPrefix(strings.TrimSpace(path), "M") {
		return Box{}, false
	}
	left, top, right, bottom := values[0], values[1], values[0], values[1]
	for i := 0; i < len(values); i += 2 {
		left, right = math.Min(left, values[i]), math.Max(right, values[i])
		top, bottom = math.Min(top, values[i+1]), math.Max(bottom, values[i+1])
	}
	return Box{X: left, Y: top, Width: right - left, Height: bottom - top}, true
}

func svgMatrix(value string) []float64 {
	if !strings.HasPrefix(value, "matrix(") || !strings.HasSuffix(value, ")") {
		return nil
	}
	parts := strings.Fields(strings.ReplaceAll(value[7:len(value)-1], ",", " "))
	if len(parts) != 6 {
		return nil
	}
	result := make([]float64, 6)
	for i, part := range parts {
		number, err := strconv.ParseFloat(part, 64)
		if err != nil || !finite(number) {
			return nil
		}
		result[i] = number
	}
	return result
}

func svgPageMatrix(value string, height float64) []float64 {
	result := svgMatrix(value)
	if result == nil || result[0] < .98 || result[0] > 1.02 || result[1] != 0 || result[2] != 0 || result[3] != -result[0] || math.Abs(result[4]) > .01 || math.Abs(result[5]-height*result[0]) > 1 {
		return nil
	}
	return result
}

func sameSVGMatrix(a, b []float64) bool {
	for i := range a {
		if math.Abs(a[i]-b[i]) > .000001 {
			return false
		}
	}
	return true
}

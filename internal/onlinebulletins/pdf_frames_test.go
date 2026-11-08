package onlinebulletins

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func scaledBackFrameSVG() string {
	return `<svg viewBox="0 0 612 792"><g>
<path fill="none" stroke="rgb(0%, 0%, 0%)" stroke-width="0.96" transform="matrix(0.75, 0, 0, 0.75, 100.2, 46.8)" d="M 0 0 C 0 -7.84 6.4 -14.08 14.08 -14.08 L 569.28 -14.08 L 569.28 -28.16 C 569.28 -36 575.52 -42.24 583.36 -42.24 C 591.2 -42.24 597.44 -36 597.44 -28.16 L 597.44 140.48 C 597.44 148.32 591.2 154.56 583.36 154.56 L 28.16 154.56 L 28.16 168.64 C 28.16 176.48 21.92 182.72 14.08 182.72 C 6.4 182.72 0 176.48 0 168.64 Z M 0 0"/>
<path fill="none" stroke="rgb(0%, 0%, 0%)" stroke-width="0.96" transform="matrix(0.75, 0, 0, 0.75, 0, 0)" d="M 50.56 483.2 L 50.56 569.76 L 751.84 569.76 L 751.84 483.2 Z M 50.56 483.2"/>
<path fill="none" stroke="rgb(0%, 0%, 0%)" stroke-width="0.96" transform="matrix(0.75, 0, 0, 0.75, 0, 0)" d="M 50 700 L 50 800 L 751 800 L 751 700 Z M 50 700"/>
</g></svg>`
}

func TestBackFrameGeometryHandlesScaledTranslatedPaths(t *testing.T) {
	frames, err := parseBackFrameSVG(strings.NewReader(scaledBackFrameSVG()), 612, 792)
	if err != nil || len(frames) != 2 {
		t.Fatalf("scaled frames unavailable: %v %v", frames, err)
	}
	for i, want := range []struct {
		element string
		box     Box
	}{
		{"summaryFrame", Box{X: 100.2, Y: 15.12, Width: 448.08, Height: 168.72}},
		{"prayersFrame", Box{X: 37.92, Y: 362.4, Width: 525.96, Height: 64.92}},
	} {
		got := frames[i]
		if got.Element != want.element || math.Abs(got.Box.X*612-want.box.X) > .001 || math.Abs(got.Box.Y*792-want.box.Y) > .001 || math.Abs(got.Box.Width*612-want.box.Width) > .001 || math.Abs(got.Box.Height*792-want.box.Height) > .001 {
			t.Fatalf("frame transform lost: %+v", got)
		}
	}
}

func TestScaledBackFramesRejectUnprovenTransformsAndDuplicates(t *testing.T) {
	source := scaledBackFrameSVG()
	for _, bad := range []string{
		strings.ReplaceAll(source, "matrix(0.75, 0, 0, 0.75", "matrix(0.75, 0.1, 0, 0.75"),
		strings.ReplaceAll(source, "matrix(0.75, 0, 0, 0.75", "matrix(0.75, 0, 0, 0.8"),
		strings.ReplaceAll(source, "matrix(0.75, 0, 0, 0.75", "matrix(NaN, 0, 0, 0.75"),
		strings.Replace(source, "<g>", `<g transform="translate(0,400)">`, 1),
		strings.Replace(source, "</svg>", strings.TrimSuffix(strings.TrimPrefix(source, `<svg viewBox="0 0 612 792">`), "</svg>")+"</svg>", 1),
	} {
		frames, err := parseBackFrameSVG(strings.NewReader(bad), 612, 792)
		if err == nil && len(frames) != 0 {
			t.Fatal("unsupported frame acquired semantic geometry")
		}
	}
}

func TestBodyFrameGeometryRequiresFourClosedEdges(t *testing.T) {
	rect := func(x, y, w, h float64) string {
		return fmt.Sprintf(`<path fill="rgb(0%%, 0%%, 0%%)" fill-opacity="1" d="M %g %g L %g %g L %g %g L %g %g Z M %g %g"/>`, x, y, x+w, y, x+w, y+h, x, y+h, x, y)
	}
	edges := rect(68, 400, .5, 18) + rect(200, 400, .5, 18) + rect(68.5, 399.5, 131.5, .5)
	wrap := func(paths string) string { return `<svg viewBox="0 0 595.32 841.92">` + paths + `</svg>` }
	for _, closed := range []bool{false, true} {
		paths := edges + rect(70, 450, 100, .5) // Unrelated underline is not a frame.
		if closed {
			paths += rect(68.5, 418, 131.5, .5)
		}
		frames, err := parseBodyFrameSVG(strings.NewReader(wrap(paths)), templateV1Width, templateV1Height)
		want := 0
		if closed {
			want = 1
		}
		if err != nil || len(frames) != want {
			t.Fatalf("closed=%v: frames=%v err=%v", closed, frames, err)
		}
		if closed && frames[0].Element != "bodyHeadingFrame" {
			t.Fatal("wrong frame role")
		}
	}
}

func TestBodyFrameGeometryAcceptsCompoundWordBorderPaths(t *testing.T) {
	source := `<svg viewBox="0 0 595.32 841.92"><path fill="rgb(0%, 0%, 0%)" fill-opacity="1" d="M 90 255 L 216 255 L 216 255.5 L 90 255.5 Z M 90 255.5 L 90.5 255.5 L 90.5 274.5 L 90 274.5 Z M 215.5 255.5 L 216 255.5 L 216 274.5 L 215.5 274.5 Z M 90 274.5 L 216 274.5 L 216 275 L 90 275 Z M 90 275"/></svg>`
	frames, err := parseBodyFrameSVG(strings.NewReader(source), templateV1Width, templateV1Height)
	if err != nil || len(frames) != 1 {
		t.Fatalf("compound border lost: %+v %v", frames, err)
	}
}

func TestBackFrameGeometryUsesNativePathsNotRasterDimensions(t *testing.T) {
	source := `<svg width="595.32" height="841.92" viewBox="0 0 595.32 841.92"><defs><path d="M 0 0 L 595.32 0 L 595.32 841.92 Z"/></defs><g>
<path fill="none" stroke="rgb(0%, 0%, 0%)" stroke-width="0.75" transform="matrix(1, 0, 0, -1, 0, 841.92)" d="M 75.75 795.57 C 75.75 802.04 80.985 807.27 87.45 807.27 L 538.75 807.27 L 538.75 818.97 C 538.75 825.44 543.98 830.67 550.45 830.67 C 556.92 830.67 562.15 825.44 562.15 818.97 L 562.15 678.57 C 562.15 672.1 556.92 666.87 550.45 666.87 L 99.15 666.87 L 99.15 655.17 C 99.15 648.70 93.915 643.47 87.45 643.47 C 80.985 643.47 75.75 648.70 75.75 655.17 Z M 75.75 795.57"/>
<path fill="rgb(100%, 100%, 100%)" fill-opacity="1" stroke="rgb(0%, 0%, 0%)" stroke-width="1" stroke-dasharray="4 3" transform="matrix(1, 0, 0, -1, 0, 841.92)" d="M 12.75 626.57 L 573.55 626.57 L 573.55 455.02 L 12.75 455.02 Z M 12.75 626.57"/>
<path fill="rgb(50.19989%, 50.19989%, 50.19989%)" fill-opacity="0.50196" d="M 18.75 390.6 L 578.2 390.6 L 578.2 462.35 L 18.75 462.35 Z M 18.75 390.6"/>
<text>DROP_private_roster</text></g></svg>`
	frames, err := parseBackFrameSVG(strings.NewReader(source), templateV1Width, templateV1Height)
	if err != nil || len(frames) != 3 {
		t.Fatalf("native frames unavailable: %v", err)
	}
	for _, frame := range frames {
		if !validBox(frame.Box) || (frame.Box.Y+frame.Box.Height)*templateV1Height > templateV1BackContentBottom {
			t.Fatal("native frame escaped retained region")
		}
	}
	if frames[0].Element != "summaryFrame" || frames[1].Element != "announcementsFrame" || frames[2].Element != "prayersFrame" {
		t.Fatal("native frame roles changed")
	}
	for _, invalid := range []string{
		strings.Replace(source, "462.35", "600", 1),
		strings.Replace(source, "matrix(1, 0, 0, -1", "matrix(1, 1, 0, -1", 1),
		`<!DOCTYPE svg [<!ENTITY external SYSTEM "file:///private">]>` + source,
		source + source,
	} {
		frames, err := parseBackFrameSVG(strings.NewReader(invalid), templateV1Width, templateV1Height)
		if err == nil && len(frames) == 3 {
			t.Fatal("unsupported or excluded vector silently accepted")
		}
	}
	for _, malformed := range []string{"ZZZZZZZZZ", "M 1 2 L 3", "M NaN 2 Z", "M 1 2 A 3 4 5 6 7 8 9 Z"} {
		if _, valid := svgPathBounds(malformed); valid {
			t.Fatal("malformed native path accepted")
		}
	}
}

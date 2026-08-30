package content

import (
	"slices"
	"testing"
)

func TestNewPageGroupManifestIsCanonical(t *testing.T) {
	left := []PageGroupManifestItem{
		{ID: "video-b", SourceVersion: 4, TargetVersion: 5, Action: GroupActionPublish},
		{ID: "video-a", SourceVersion: 2, TargetVersion: 2, Action: GroupActionKeep},
	}
	right := []PageGroupManifestItem{left[1], left[0]}
	one, err := NewPageGroupManifest("page-home", 8, 9, ModuleVideos, left)
	if err != nil {
		t.Fatal(err)
	}
	two, err := NewPageGroupManifest("page-home", 8, 9, ModuleVideos, right)
	if err != nil {
		t.Fatal(err)
	}
	if one.SHA256 != two.SHA256 || !slices.Equal(one.Items, two.Items) {
		t.Fatalf("one=%#v two=%#v", one, two)
	}
}

func TestPageGroupForChildIsFixed(t *testing.T) {
	for module, want := range map[Module]string{ModuleVideos: "home", ModuleHistory: "about"} {
		if got, ok := PageGroupForChild(module); !ok || got != want {
			t.Fatalf("%s -> %q %t", module, got, ok)
		}
	}
	if _, ok := PageGroupForChild(ModuleNews); ok {
		t.Fatal("news must not join a Page group")
	}
}

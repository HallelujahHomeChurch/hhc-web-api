package content

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

type GroupAction string

const (
	GroupActionKeep    GroupAction = "keep"
	GroupActionPublish GroupAction = "publish"
	GroupActionRemove  GroupAction = "remove"
)

type PageGroupManifestItem struct {
	ID            string      `json:"id"`
	SourceVersion int64       `json:"sourceVersion"`
	TargetVersion int64       `json:"targetVersion"`
	Action        GroupAction `json:"action"`
}

type PageGroupManifest struct {
	PageID            string                  `json:"pageId"`
	PageSourceVersion int64                   `json:"pageSourceVersion"`
	PageTargetVersion int64                   `json:"pageTargetVersion"`
	ChildModule       Module                  `json:"childModule"`
	Items             []PageGroupManifestItem `json:"items"`
	SHA256            string                  `json:"sha256"`
}

func PageGroupForChild(module Module) (string, bool) {
	switch module {
	case ModuleVideos:
		return "home", true
	case ModuleHistory:
		return "about", true
	default:
		return "", false
	}
}

func NewPageGroupManifest(pageID string, source, target int64, module Module, items []PageGroupManifestItem) (PageGroupManifest, error) {
	if pageID == "" || source < 1 || target < source {
		return PageGroupManifest{}, ErrInvalid
	}
	if _, ok := PageGroupForChild(module); !ok {
		return PageGroupManifest{}, ErrInvalid
	}
	items = slices.Clone(items)
	slices.SortFunc(items, func(a, b PageGroupManifestItem) int { return strings.Compare(a.ID, b.ID) })
	for _, item := range items {
		if item.ID == "" || item.SourceVersion < 1 || item.TargetVersion < item.SourceVersion ||
			(item.Action != GroupActionKeep && item.Action != GroupActionPublish && item.Action != GroupActionRemove) {
			return PageGroupManifest{}, ErrInvalid
		}
	}
	type manifestBody struct {
		PageID            string                  `json:"pageId"`
		PageSourceVersion int64                   `json:"pageSourceVersion"`
		PageTargetVersion int64                   `json:"pageTargetVersion"`
		ChildModule       Module                  `json:"childModule"`
		Items             []PageGroupManifestItem `json:"items"`
	}
	body := manifestBody{
		PageID:            pageID,
		PageSourceVersion: source,
		PageTargetVersion: target,
		ChildModule:       module,
		Items:             items,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return PageGroupManifest{}, err
	}
	digest := sha256.Sum256(encoded)
	return PageGroupManifest{PageID: pageID, PageSourceVersion: source, PageTargetVersion: target, ChildModule: module, Items: items, SHA256: hex.EncodeToString(digest[:])}, nil
}

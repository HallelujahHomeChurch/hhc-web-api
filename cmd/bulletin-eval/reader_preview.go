package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

// Inputs are server-owned revision documents from the same source PDF.
// The report contains reading anchors only and never reads member notes.
func previewReader(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("preview-reader", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	published := flags.String("published", "", "current private revision JSON")
	candidate := flags.String("candidate", "", "proposed private revision JSON")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errEvaluation
	}
	read := func(path string) (onlinebulletins.Document, error) {
		var document onlinebulletins.Document
		file, err := os.Open(path)
		if err != nil {
			return document, err
		}
		defer file.Close()
		return onlinebulletins.DecodeDocument(file)
	}
	before, err := read(*published)
	if err != nil {
		return err
	}
	after, err := read(*candidate)
	if err != nil {
		return err
	}
	if before.IssueID != after.IssueID || before.Series != after.Series || before.ContentLocale != after.ContentLocale || before.SourceAssetChecksum != after.SourceAssetChecksum {
		return errEvaluation
	}
	type navigation struct {
		Topics []string       `json:"topicBlockIds"`
		Pages  map[string]int `json:"pdfPageStarts"`
	}
	project := func(doc onlinebulletins.Document) (navigation, error) {
		c, err := onlinebulletins.ProjectMemberContent(doc)
		return navigation{Topics: c.TopicBlockIDs, Pages: c.PDFPageStarts}, err
	}
	current, err := project(before)
	if err != nil {
		return err
	}
	next, err := project(after)
	if err != nil {
		return err
	}
	anchors := func(d onlinebulletins.Document) []onlinebulletins.ReaderAnchor {
		return onlinebulletins.ReaderAnchors(d, onlinebulletins.CanonicalMetadata{})
	}
	byID := map[string]onlinebulletins.ReaderAnchor{}
	for _, a := range anchors(after) {
		byID[a.ID] = a
	}
	changed := []string{}
	for _, a := range anchors(before) {
		b, ok := byID[a.ID]
		if !ok || a.Text != b.Text || a.ComponentID != b.ComponentID {
			changed = append(changed, a.ID)
		}
	}
	return json.NewEncoder(output).Encode(struct {
		Current         navigation `json:"current"`
		Candidate       navigation `json:"candidate"`
		AnchorsToReview []string   `json:"anchorsToReview"`
	}{current, next, changed})
}

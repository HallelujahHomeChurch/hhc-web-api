package bulletinreader

import (
	"encoding/json"
	"errors"
	"time"
)

var ErrInvalid = errors.New("invalid reader operation")
var ErrMapping = errors.New("reader mapping history unavailable")
var ErrMutationReuse = errors.New("reader mutation ID reused with different payload")

const MaxAnchors = 500
const MaxNoteLength = 10000
const ReplayWindow = 90 * 24 * time.Hour
const MutationRetention = 91 * 24 * time.Hour

type DirectMapping struct {
	FromSentenceID string   `json:"fromSentenceId"`
	ToSentenceIDs  []string `json:"toSentenceIds"`
}
type StateResponse struct {
	State        State           `json:"state"`
	FromRevision *int64          `json:"fromRevision,omitempty"`
	Mappings     []DirectMapping `json:"mappings,omitempty"`
}
type MutationResponse struct {
	State   State    `json:"state"`
	Results []Result `json:"results"`
}

type Anchor struct {
	ID          string `json:"sentenceId"`
	Text        string `json:"quote"`
	ComponentID string `json:"componentId"`
	PageID      string `json:"pageId"`
}
type SourceFragment struct {
	PageID     string
	Start, End int
}
type Snapshot struct {
	DocumentID        string
	Revision          int64
	Pages, Components []string
	Sentences         []Anchor
	SentencePages     map[string][]string
	SentenceFragments map[string][]SourceFragment
}
type Highlight struct {
	Segments   []HighlightSegment `json:"segments,omitempty"`
	SentenceID string             `json:"sentenceId"`
	Color      string             `json:"color"`
	Quote      string             `json:"quote"`
	Active     bool               `json:"active"`
	Version    int64              `json:"version"`
	UpdatedAt  time.Time          `json:"updatedAt"`
}
type HighlightHistoryEntry struct {
	ID        string     `json:"id"`
	Highlight *Highlight `json:"highlight,omitempty"`
}
type Note struct {
	Ranges           []NoteRange `json:"ranges,omitempty"`
	ID               string      `json:"id"`
	Text             string      `json:"text"`
	SentenceIDs      []string    `json:"sentenceIds"`
	InactiveAnchors  []Anchor    `json:"inactiveAnchors"`
	Quote            string      `json:"quote"`
	Version          int64       `json:"version"`
	Deleted          bool        `json:"deleted"`
	ReanchorRequired bool        `json:"reanchorRequired"`
	CreatedAt        time.Time   `json:"createdAt"`
	UpdatedAt        time.Time   `json:"updatedAt"`
}
type Bookmark struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	SentenceID       string    `json:"sentenceId"`
	Offset           int       `json:"offset"`
	PageID           string    `json:"pageId"`
	DocumentRevision int64     `json:"documentRevision"`
	Version          int64     `json:"version"`
	Deleted          bool      `json:"deleted"`
	ReanchorRequired bool      `json:"reanchorRequired"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}
type Progress struct {
	PageID      string    `json:"pageId"`
	ComponentID string    `json:"componentId"`
	SentenceID  string    `json:"sentenceId"`
	RecordedAt  time.Time `json:"recordedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
type MigrationConflict struct {
	ID          string      `json:"id"`
	SentenceIDs []string    `json:"sentenceIds"`
	Sources     []Highlight `json:"sources"`
}
type State struct {
	OwnerID          string                  `json:"accountId"`
	DocumentID       string                  `json:"documentId"`
	AppliedRevision  int64                   `json:"appliedRevision"`
	CurrentRevision  int64                   `json:"currentRevision"`
	Highlights       []Highlight             `json:"highlights"`
	HighlightHistory []HighlightHistoryEntry `json:"highlightHistory,omitempty"`
	Notes            []Note                  `json:"notes"`
	Bookmarks        []Bookmark              `json:"bookmarks"`
	Progress         *Progress               `json:"progress"`
	Conflicts        []MigrationConflict     `json:"conflicts"`
}
type Mutation struct {
	MutationID       string          `json:"mutationId"`
	CreatedAt        time.Time       `json:"createdAt"`
	DocumentRevision int64           `json:"documentRevision"`
	Kind             string          `json:"kind"`
	BaseVersion      *int64          `json:"baseVersion,omitempty"`
	Payload          json.RawMessage `json:"payload"`
}
type Result struct {
	MutationID string    `json:"mutationId"`
	Status     string    `json:"status"`
	Revision   int64     `json:"revision"`
	Note       *Note     `json:"note,omitempty"`
	Bookmark   *Bookmark `json:"bookmark,omitempty"`
}

func NewState(owner, document string, revision int64) State {
	return State{OwnerID: owner, DocumentID: document, AppliedRevision: revision, CurrentRevision: revision, Highlights: []Highlight{}, Notes: []Note{}, Bookmarks: []Bookmark{}, Conflicts: []MigrationConflict{}}
}

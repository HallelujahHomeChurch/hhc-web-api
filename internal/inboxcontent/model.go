package inboxcontent

import "github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"

type Resource struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Series string `json:"series,omitempty"`
}
type Ref struct {
	ID       string   `json:"id"`
	Resource Resource `json:"resource"`
}
type Request struct {
	ActorID string `json:"actorId"`
	Locale  string `json:"locale"`
	Items   []Ref  `json:"items"`
}
type Action struct {
	Type     string   `json:"type"`
	Resource Resource `json:"resource"`
	Locale   string   `json:"locale,omitempty"`
}
type Projection struct {
	TitleKey   string            `json:"titleKey"`
	MessageKey string            `json:"messageKey"`
	Parameters map[string]string `json:"parameters"`
	Action     Action            `json:"action"`
}
type Decision struct {
	ID         string      `json:"id"`
	Visible    bool        `json:"visible"`
	Projection *Projection `json:"projection,omitempty"`
}
type Bulletin struct {
	bulletins.ProtectedBulletin
	Online bool
}
type Entitlement struct{ Series, Locale, Code string }

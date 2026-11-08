package platform

import "time"

type OperationProgress struct {
	Status       string    `json:"status"`
	Stage        string    `json:"stage"`
	Percent      int       `json:"percent"`
	Completed    *int      `json:"completed,omitempty"`
	Total        *int      `json:"total,omitempty"`
	ErrorCode    string    `json:"errorCode,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
	RetryAfterMS int       `json:"retryAfterMs"`
}

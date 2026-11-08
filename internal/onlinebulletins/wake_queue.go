package onlinebulletins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue/v2"
)

const WakeVisibility = 180 * time.Second
const wakePayload = `{"version":1}`

var errInvalidWake = errors.New("invalid extraction wake")

type WakeMessage struct{ ID, Receipt string }
type WakeQueue struct {
	main, poison *azqueue.QueueClient
	visibility   time.Duration
}

func extractionWakeVisibility(recognition bool) time.Duration {
	if recognition {
		return 13 * time.Minute
	}
	return WakeVisibility
}

func validWake(payload []byte) bool {
	var value struct {
		Version int `json:"version"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	return decoder.Decode(&value) == nil && value.Version == 1 && decoder.Decode(new(any)) == io.EOF
}

func NewWakeQueue(queueURL, poisonURL string, recognition bool) (*WakeQueue, error) {
	for _, raw := range []string{queueURL, poisonURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errInvalidWake
		}
	}
	credential, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, err
	}
	main, err := azqueue.NewQueueClient(queueURL, credential, nil)
	if err != nil {
		return nil, err
	}
	poison, err := azqueue.NewQueueClient(poisonURL, credential, nil)
	if err != nil {
		return nil, err
	}
	return &WakeQueue{main: main, poison: poison, visibility: extractionWakeVisibility(recognition)}, nil
}

func (q *WakeQueue) Enqueue(ctx context.Context, payload []byte) error {
	if !validWake(payload) {
		return errInvalidWake
	}
	_, err := q.main.EnqueueMessage(ctx, wakePayload, nil)
	return err
}

func (q *WakeQueue) Receive(ctx context.Context) (WakeMessage, bool, error) {
	result, err := q.main.DequeueMessage(ctx, &azqueue.DequeueMessageOptions{VisibilityTimeout: to.Ptr(int32(q.visibility / time.Second))})
	if err != nil || len(result.Messages) == 0 {
		return WakeMessage{}, false, err
	}
	raw := result.Messages[0]
	if raw.MessageID == nil || raw.PopReceipt == nil {
		return WakeMessage{}, false, errInvalidWake
	}
	message := WakeMessage{*raw.MessageID, *raw.PopReceipt}
	if raw.MessageText == nil || !validWake([]byte(*raw.MessageText)) {
		return message, true, errInvalidWake
	}
	return message, true, nil
}

func (q *WakeQueue) Ack(ctx context.Context, message WakeMessage) error {
	_, err := q.main.DeleteMessage(ctx, message.ID, message.Receipt, nil)
	return err
}
func (q *WakeQueue) Defer(ctx context.Context, message WakeMessage, delay time.Duration) error {
	delay = max(time.Second, min(delay, q.visibility))
	seconds := int32((delay + time.Second - 1) / time.Second)
	_, err := q.main.UpdateMessage(ctx, message.ID, message.Receipt, wakePayload, &azqueue.UpdateMessageOptions{VisibilityTimeout: to.Ptr(seconds)})
	return err
}
func (q *WakeQueue) Poison(ctx context.Context, message WakeMessage) error {
	// Do not copy malformed queue contents into a retained diagnostic queue.
	if _, err := q.poison.EnqueueMessage(ctx, `{"invalidWake":true}`, nil); err != nil {
		return err
	}
	return q.Ack(ctx, message)
}

type ExtractionWakeQueue interface {
	Receive(context.Context) (WakeMessage, bool, error)
	Ack(context.Context, WakeMessage) error
	Defer(context.Context, WakeMessage, time.Duration) error
	Poison(context.Context, WakeMessage) error
}

// DB state owns all extraction/layout/conversion work. Preserve the wake for
// remaining work, delayed retries or a crashed execution's unexpired lease.
func RunExtractionWake(ctx context.Context, queue ExtractionWakeQueue, batch func(context.Context) error, next func(context.Context) (time.Time, error)) error {
	message, found, err := queue.Receive(ctx)
	if !found {
		return err
	}
	if errors.Is(err, errInvalidWake) {
		return queue.Poison(ctx, message)
	}
	if err != nil {
		return err
	}
	if err = batch(ctx); err != nil {
		return err
	}
	pending, err := next(ctx)
	if err != nil {
		return err
	}
	if !pending.IsZero() {
		return queue.Defer(ctx, message, time.Until(pending))
	}
	if err = queue.Ack(ctx, message); err == nil {
		slog.Info("bulletin extraction wake completed")
	}
	return err
}

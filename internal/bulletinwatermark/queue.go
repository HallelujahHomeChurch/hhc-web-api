package bulletinwatermark

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue/v2"
	"github.com/google/uuid"
)

// DequeueVisibility hides a delivered message for one execution. It equals
// the Job replicaTimeout and exceeds the DB lease (postgres downloadLease,
// 270s), so a killed execution's message reappears after its lease lapsed and
// the next execution can claim the job immediately (PLAN-v2 W5-3).
const DequeueVisibility = 300 * time.Second

type WorkMessage struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type LeasedMessage struct {
	WorkMessage
	MessageID, PopReceipt string
	DequeueCount          int64
}

type AzureQueue struct {
	main, poison *azqueue.QueueClient
}

func NewAzureQueue(queueURL, poisonURL string) (*AzureQueue, error) {
	if !strings.HasPrefix(queueURL, "https://") || !strings.HasPrefix(poisonURL, "https://") {
		return nil, errors.New("watermark queue URLs must use HTTPS")
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
	return &AzureQueue{main: main, poison: poison}, nil
}

func validWorkMessage(v WorkMessage) bool {
	return (v.Kind == "download" || v.Kind == "investigation") && uuid.Validate(v.ID) == nil
}

func (q *AzureQueue) Enqueue(ctx context.Context, payload []byte) error {
	var message WorkMessage
	if json.Unmarshal(payload, &message) != nil || !validWorkMessage(message) {
		return errors.New("invalid watermark work message")
	}
	_, err := q.main.EnqueueMessage(ctx, string(payload), nil)
	return err
}

// Peek reads the main queue without dequeuing or changing visibility. The
// worker preflight uses it to prove queue read access with no side effect.
func (q *AzureQueue) Peek(ctx context.Context) error {
	_, err := q.main.PeekMessage(ctx, nil)
	return err
}

func (q *AzureQueue) Dequeue(ctx context.Context) (LeasedMessage, bool, error) {
	response, err := q.main.DequeueMessage(ctx, &azqueue.DequeueMessageOptions{VisibilityTimeout: to.Ptr(int32(DequeueVisibility / time.Second))})
	if err != nil || len(response.Messages) == 0 {
		return LeasedMessage{}, false, err
	}
	raw := response.Messages[0]
	if raw.MessageID == nil || raw.PopReceipt == nil || raw.MessageText == nil || raw.DequeueCount == nil {
		return LeasedMessage{}, false, errors.New("incomplete watermark queue message")
	}
	leased := LeasedMessage{MessageID: *raw.MessageID, PopReceipt: *raw.PopReceipt, DequeueCount: *raw.DequeueCount}
	if json.Unmarshal([]byte(*raw.MessageText), &leased.WorkMessage) != nil || !validWorkMessage(leased.WorkMessage) {
		return leased, true, errors.New("invalid watermark queue message")
	}
	return leased, true, nil
}

func (q *AzureQueue) Ack(ctx context.Context, message LeasedMessage) error {
	_, err := q.main.DeleteMessage(ctx, message.MessageID, message.PopReceipt, nil)
	return err
}

func (q *AzureQueue) Poison(ctx context.Context, message LeasedMessage) error {
	payload, _ := json.Marshal(message.WorkMessage)
	if _, err := q.poison.EnqueueMessage(ctx, string(payload), nil); err != nil {
		return err
	}
	return q.Ack(ctx, message)
}

// Retry hides the message for delay so the queue redelivers it. The original
// work payload is kept; DequeueCount still increases on the next delivery.
func (q *AzureQueue) Retry(ctx context.Context, message LeasedMessage, delay time.Duration) error {
	payload, _ := json.Marshal(message.WorkMessage)
	seconds := int32(delay / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	_, err := q.main.UpdateMessage(ctx, message.MessageID, message.PopReceipt, string(payload), &azqueue.UpdateMessageOptions{VisibilityTimeout: to.Ptr(seconds)})
	return err
}

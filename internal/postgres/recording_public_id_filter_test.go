package postgres

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRecordingPublicIDFilter(t *testing.T) {
	for _, blocked := range recordingPublicIDBlockedStrings {
		if !inappropriateRecordingPublicID("x" + strings.ToUpper(blocked) + "x") {
			t.Errorf("accepted listed string %q", blocked)
		}
	}
	for _, id := range []string{"xxFuCkxxxxx", "xF_u-C_kxxx", "xx5h1txxxxx", "xx5h17xxxxx", "xxs3xxxxxxx", "xxp0rnxxxxx", "xxN4Z1xxxxx", "xxG8xxxxxxx", "xxGYxxxxxxx", "xxTMDxxxxxx", "xxNMSLxxxxx", "xxCNMxxxxxx", "xxFCKxxxxxx", "xxFCUKxxxxx"} {
		if !inappropriateRecordingPublicID(id) {
			t.Errorf("accepted %q", id)
		}
	}
	for _, id := range []string{"abcdefghijk", "ABCDEFGHIJK", "01_234-6789", "xxLMAOxxxxx"} {
		if inappropriateRecordingPublicID(id) {
			t.Errorf("rejected %q", id)
		}
	}
}

func publicIDEntropy(t *testing.T, ids ...string) []byte {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"
	var data []byte
	for _, id := range ids {
		if len(id) != 11 {
			t.Fatalf("invalid test candidate %q", id)
		}
		for _, c := range id {
			data = append(data, byte(strings.IndexRune(alphabet, c)))
		}
	}
	return data
}

func TestRecordingPublicIDGenerationRejectsThenRetries(t *testing.T) {
	reader := bytes.NewReader(publicIDEntropy(t, "xxFuCkxxxxx", "xF_u-C_kxxx", "xx5h1txxxxx", "abcdefghijk"))
	id, err := recordingPublicIDFromEntropy(reader)
	if err != nil || id != "abcdefghijk" || reader.Len() != 0 {
		t.Fatalf("id=%q err=%v remaining=%d", id, err, reader.Len())
	}
}

func TestRecordingPublicIDGenerationBoundedAndEntropyErrors(t *testing.T) {
	reader := bytes.NewReader(bytes.Repeat(publicIDEntropy(t, "xxFuCkxxxxx"), 33))
	id, err := recordingPublicIDFromEntropy(reader)
	if id != "" || err == nil || reader.Len() != 11 {
		t.Fatalf("id=%q err=%v remaining=%d", id, err, reader.Len())
	}
	for _, input := range [][]byte{nil, {0}, publicIDEntropy(t, "xxFuCkxxxxx")} {
		id, err := recordingPublicIDFromEntropy(bytes.NewReader(input))
		if id != "" || (!errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF)) {
			t.Fatalf("id=%q err=%v", id, err)
		}
	}
}

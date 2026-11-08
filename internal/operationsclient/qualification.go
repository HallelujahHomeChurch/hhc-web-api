package operationsclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"io"
)

type MembershipQualification struct {
	SubjectID string `json:"subjectId"`
	Qualified bool   `json:"qualified"`
	Version   string `json:"version"`
}

func (c *Client) CheckMemberQualification(ctx context.Context, subject string) (MembershipQualification, error) {
	id, err := uuid.Parse(subject)
	if err != nil || id.String() != subject || (id.Version() != 4 && id.Version() != 7) {
		return MembershipQualification{}, fmt.Errorf("invalid qualification subject")
	}
	body, _ := json.Marshal(map[string]string{"subjectId": subject})
	request, err := c.request(ctx, "/priv/operations/member-qualifications", body)
	if err != nil {
		return MembershipQualification{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return MembershipQualification{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return MembershipQualification{}, fmt.Errorf("qualification authority status %d", response.StatusCode)
	}
	var wire struct {
		SubjectID string `json:"subjectId"`
		Qualified *bool  `json:"qualified"`
		Version   string `json:"version"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return MembershipQualification{}, fmt.Errorf("invalid qualification response")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return MembershipQualification{}, fmt.Errorf("invalid qualification response")
	}
	digest, e := hex.DecodeString(wire.Version)
	if wire.SubjectID != subject || wire.Qualified == nil || e != nil || len(digest) != 32 {
		return MembershipQualification{}, fmt.Errorf("invalid qualification response")
	}
	return MembershipQualification{wire.SubjectID, *wire.Qualified, wire.Version}, nil
}

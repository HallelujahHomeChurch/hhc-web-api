package onlinebulletins

import "context"

// Every provider attempt is recorded before further spending. A verification
// retry is bounded to one; source/lease/transport failures never trigger a cascade.
func (c *AzureExtractor) RecognizeVerifiedPage(ctx context.Context, input RecognitionPage, record func(string, RecognitionResult, RecognitionUsage, RecognitionUsage, string) error) (RecognitionResult, RecognitionUsage, error) {
	var total RecognitionUsage
	save := func(phase string, result RecognitionResult, usage RecognitionUsage, err error) error {
		total.InputTokens += usage.InputTokens
		total.OutputTokens += usage.OutputTokens
		total.ReasoningTokens += usage.ReasoningTokens
		total.Requests += usage.Requests
		total.Duration += usage.Duration
		changedModel := total.Model != "" && usage.Model != "" && total.Model != usage.Model
		if changedModel {
			err = ErrRecognition
		}
		if usage.Model != "" && !changedModel {
			total.Model = usage.Model
		}
		code := ""
		if err != nil {
			code = "recognition_failed"
		}
		if record != nil {
			if changedModel && phase == "verified" {
				phase = "verification"
			}
			if recordErr := record(phase, result, usage, total, code); recordErr != nil {
				return recordErr
			}
		}
		if changedModel {
			return ErrRecognition
		}
		return nil
	}

	first, usage, err := c.RecognizePage(ctx, input)
	if saveErr := save("recognition", first, usage, err); saveErr != nil {
		return first, total, saveErr
	}
	if err != nil && usage.Model == "" {
		return first, total, err
	}
	firstSHA, firstOK := recognitionSemanticSHA256(input, first)
	firstOK = firstOK && err == nil && len(first.UnassignedIDs) == 0
	// The second request has original source and continuation, but no prior proposal.
	result, usage, err := c.RecognizePage(ctx, input)
	secondSHA, secondOK := recognitionSemanticSHA256(input, result)
	secondOK = secondOK && err == nil && len(result.UnassignedIDs) == 0
	agreed := firstOK && secondOK && firstSHA == secondSHA
	phase := "verification"
	if agreed {
		result.SemanticSHA256 = secondSHA
		phase = "verified"
	} else if err == nil {
		err = ErrRecognition
	}
	if saveErr := save(phase, result, usage, err); saveErr != nil {
		return result, total, saveErr
	}
	if agreed {
		return result, total, nil
	}
	if usage.Model == "" {
		return result, total, err
	}
	// One repair may resolve the disagreement; it must agree with a valid independent pass.
	reference, referenceSHA, referenceOK := result, secondSHA, secondOK
	if !referenceOK {
		reference, referenceSHA, referenceOK = first, firstSHA, firstOK
	}
	repaired, usage, err := c.VerifyPage(ctx, input, reference)
	repairedSHA, repairedOK := recognitionSemanticSHA256(input, repaired)
	agreed = referenceOK && repairedOK && err == nil && len(repaired.UnassignedIDs) == 0 && repairedSHA == referenceSHA
	phase = "verification"
	if agreed {
		repaired.SemanticSHA256 = repairedSHA
		phase = "verified"
	} else if err == nil {
		err = ErrRecognition
	}
	if saveErr := save(phase, repaired, usage, err); saveErr != nil {
		return repaired, total, saveErr
	}
	if agreed {
		return repaired, total, nil
	}
	return repaired, total, ErrRecognition
}

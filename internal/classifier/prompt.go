package classifier

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const systemPrompt = `You are a job search email classifier. You will receive one or more emails. Classify each one.

Return ONLY a valid JSON array with no markdown formatting, no code blocks, no extra text. Each element must be:
{"relevant": true/false, "category": "interview_invite|offer|rejection|next_round|assessment|application_received|other", "summary": "one sentence summary"}

Categories:
- interview_invite: scheduling an interview (phone screen, technical, onsite, virtual)
- offer: job offer or compensation discussion
- rejection: application rejected or position filled
- next_round: advancing to next stage
- assessment: coding challenge, take-home, or test
- application_received: confirmation that application was received
- other: job-related but doesn't fit above categories

If an email is NOT related to job applications (newsletters, promotions, social media, etc.), return:
{"relevant": false, "category": "other", "summary": "not job related"}

Return exactly one JSON object per email, in the same order. Always return a JSON array, even for a single email.`

func buildBatchUserPrompt(inputs []EmailInput) string {
	var b strings.Builder
	for i, input := range inputs {
		b.WriteString(fmt.Sprintf("--- Email %d ---\nSubject: %s\nFrom: %s\nBody preview: %s\n\n", i+1, input.Subject, input.From, input.BodyPreview))
	}
	return b.String()
}

var jsonArrayRegex = regexp.MustCompile(`\[[\s\S]*\]`)

func parseBatchClassification(raw string, expected int) ([]Classification, error) {
	raw = strings.TrimSpace(raw)

	var results []Classification
	if err := json.Unmarshal([]byte(raw), &results); err == nil {
		if len(results) == expected {
			return results, nil
		}
	}

	// Fallback: extract JSON array from markdown code blocks
	if match := jsonArrayRegex.FindString(raw); match != "" {
		var results []Classification
		if err := json.Unmarshal([]byte(match), &results); err == nil {
			if len(results) == expected {
				return results, nil
			}
		}
	}

	return nil, fmt.Errorf("classifier: could not parse batch response (expected %d results): %s", expected, truncate(raw, 300))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

package gmail

import "strings"

func KeywordFilter(emails []Email, keywords []string) []Email {
	var matched []Email
	for _, e := range emails {
		subjectLower := strings.ToLower(e.Subject)
		bodyLower := strings.ToLower(e.BodyPreview)
		fromLower := strings.ToLower(e.From)

		for _, kw := range keywords {
			kwLower := strings.ToLower(kw)
			if strings.Contains(subjectLower, kwLower) ||
				strings.Contains(bodyLower, kwLower) ||
				strings.Contains(fromLower, kwLower) {
				matched = append(matched, e)
				break
			}
		}
	}
	return matched
}

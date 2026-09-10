package gmail

import "strings"

func KeywordFilter(emails []Email, keywords []string) []Email {
	if len(emails) == 0 || len(keywords) == 0 {
		return nil
	}

	normalizedKeywords := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(strings.ToLower(keyword))
		if keyword != "" {
			normalizedKeywords = append(normalizedKeywords, keyword)
		}
	}

	if len(normalizedKeywords) == 0 {
		return nil
	}

	matched := make([]Email, 0)

	for _, email := range emails {
		subject := strings.ToLower(email.Subject)
		body := strings.ToLower(email.BodyPreview)
		from := strings.ToLower(email.From)

		for _, keyword := range normalizedKeywords {
			if strings.Contains(subject, keyword) ||
				strings.Contains(body, keyword) ||
				strings.Contains(from, keyword) {
				matched = append(matched, email)
				break
			}
		}
	}

	return matched
}

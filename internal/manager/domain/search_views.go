package domain

import "strings"

func DefaultSearchMessageView(role string) string {
	if role == "assistant" {
		return "last"
	}
	return "full"
}

// SearchMessageView selects paragraphs of one assembled visible message.
// Transport parts and source-version identity are independent of this view.
func SearchMessageView(body, view string) (string, error) {
	switch view {
	case "full":
		return body, nil
	case "first":
		text := strings.TrimLeft(body, "\r\n \t")
		if index := strings.Index(text, "\n\n"); index >= 0 {
			text = text[:index]
		}
		return text, nil
	case "last":
		text := strings.TrimRight(body, "\r\n \t")
		if index := strings.LastIndex(text, "\n\n"); index >= 0 {
			text = text[index+2:]
		}
		return text, nil
	default:
		return "", ErrConflict
	}
}

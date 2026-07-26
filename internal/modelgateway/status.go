package modelgateway

import (
	"net/url"
	"strings"
)

// displayBaseURL removes URL components that could carry credentials before a
// configured endpoint is returned to the desktop UI.
func displayBaseURL(raw string) string {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target.Scheme == "" || target.Host == "" {
		return ""
	}
	target.User = nil
	target.RawQuery = ""
	target.ForceQuery = false
	target.Fragment = ""
	return strings.TrimRight(target.String(), "/")
}

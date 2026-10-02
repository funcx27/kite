package render

import "regexp"

// dns1123Pattern matches an RFC 1123 label: lowercase alphanumerics and
// dashes, starting and ending with an alphanumeric.
var dns1123Pattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ValidateDNS1123Label reports whether value is a DNS-1123 label of at most
// maxLength characters.
func ValidateDNS1123Label(value string, maxLength int) bool {
	return len(value) >= 1 && len(value) <= maxLength && dns1123Pattern.MatchString(value)
}

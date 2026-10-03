package metrics

import (
	"regexp"
	"strconv"
	"strings"
)

// MatchAnyOf returns a PromQL regex matcher `label=~"^(a|b)$"` that matches
// exactly one of values, wrapped as prefix(...)suffix (prefix/suffix are RE2
// written by the caller, e.g. "httproute/" and "/rule/[0-9]+"). Each value is
// regex-escaped, then the whole pattern is rendered as a PromQL string literal:
// PromQL strings take Go escapes, so a bare QuoteMeta `\.` (any dotted route
// or pod name) is an invalid escape and the query is rejected outright.
func MatchAnyOf(label, prefix string, values []string, suffix string) string {
	alts := make([]string, len(values))
	for i, v := range values {
		alts[i] = regexp.QuoteMeta(v)
	}
	return label + "=~" + strconv.Quote("^"+prefix+"("+strings.Join(alts, "|")+")"+suffix+"$")
}

// Equals returns a PromQL equality matcher `label="value"` with value rendered
// as a PromQL string literal.
func Equals(label, value string) string {
	return label + "=" + strconv.Quote(value)
}

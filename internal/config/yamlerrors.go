package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// The fleet file's YAML errors (Settings slice 0, Codex's re-reviews): a
// YAML error reaches no one - Settings, a panel, a writer's refusal, the
// log - with the file's content in it. Every decode of the file goes
// through unmarshalFleet, and its error through readableYAML, which passes
// on only what is known to be content-free and rewrites everything else to
// its kind and line. It is an allowlist, not a list of the forms seen, so a
// form not reviewed is withheld, never shown. Reviewed against yaml.v3
// v3.0.1's producers (scannerc.go, parserc.go, readerc.go, decode.go,
// resolve.go); a version change re-reviews them.

// unmarshalFleet decodes the fleet file's bytes: the loader's decode, the
// key walk's and each writer's round-trip check.
func unmarshalFleet(b []byte, v any) error {
	return readableYAML(yaml.Unmarshal(b, v))
}

// yamlProblems are yaml.v3's parser, scanner and reader problems: fixed
// strings, extracted from v3.0.1's sources.
var yamlProblems = map[string]bool{
	"block sequence entries are not allowed in this context":       true,
	"control characters are not allowed":                           true,
	"could not find expected ':'":                                  true,
	"could not find expected directive name":                       true,
	"did not find URI escaped octet":                               true,
	"did not find expected '!'":                                    true,
	"did not find expected ',' or ']'":                             true,
	"did not find expected ',' or '}'":                             true,
	"did not find expected '-' indicator":                          true,
	"did not find expected <document start>":                       true,
	"did not find expected <stream-start>":                         true,
	"did not find expected alphabetic or numeric character":        true,
	"did not find expected comment or line break":                  true,
	"did not find expected digit or '.' character":                 true,
	"did not find expected hexdecimal number":                      true,
	"did not find expected key":                                    true,
	"did not find expected node content":                           true,
	"did not find expected tag URI":                                true,
	"did not find expected version number":                         true,
	"did not find expected whitespace":                             true,
	"did not find expected whitespace or line break":               true,
	"did not find the expected '>'":                                true,
	"expected low surrogate area":                                  true,
	"found a tab character that violates indentation":              true,
	"found a tab character where an indentation space is expected": true,
	"found an incorrect leading UTF-8 octet":                       true,
	"found an incorrect trailing UTF-8 octet":                      true,
	"found an indentation indicator equal to 0":                    true,
	"found character that cannot start any token":                  true,
	"found duplicate %TAG directive":                               true,
	"found duplicate %YAML directive":                              true,
	"found extremely long version number":                          true,
	"found incompatible YAML document":                             true,
	"found invalid Unicode character escape code":                  true,
	"found undefined tag handle":                                   true,
	"found unexpected document indicator":                          true,
	"found unexpected end of stream":                               true,
	"found unexpected non-alphabetical character":                  true,
	"found unknown directive name":                                 true,
	"found unknown escape character":                               true,
	"incomplete UTF-16 character":                                  true,
	"incomplete UTF-16 surrogate pair":                             true,
	"incomplete UTF-8 octet sequence":                              true,
	"invalid Unicode character":                                    true,
	"invalid leading UTF-8 octet":                                  true,
	"invalid length of a UTF-8 sequence":                           true,
	"invalid trailing UTF-8 octet":                                 true,
	"mapping keys are not allowed in this context":                 true,
	"mapping values are not allowed in this context":               true,
	"unexpected low surrogate area":                                true,
	"unknown problem parsing YAML content":                         true,
}

// yamlDecoderFixed are the decoder's own content-free messages.
var yamlDecoderFixed = map[string]bool{
	"attempted to go past the end of stream; corrupted value?": true,
	"document contains excessive aliasing":                     true,
	"!!binary value contains invalid base64 data":              true,
	"map merge requires map or sequence of maps as the value":  true,
}

var (
	// a problem with its line: "line N: problem"
	yamlAtLine = regexp.MustCompile(`^line (\d+): (.*)$`)
	// the dynamic content-free problems: depth limits, event names, counts
	yamlDynamicFixed = regexp.MustCompile(`^(exceeded max depth of \d+|expected [A-Za-z_ -]+ event but got [A-Za-z_ -]+|cannot decode node with unknown kind \d+|invalid array: want \d+ elements but got \d+)$`)
	// resolve.go: a tagged scalar its tag cannot hold, quoted in full
	yamlResolve = regexp.MustCompile(`^cannot decode (\S+) .* as a (\S+)$`)
	yamlTag     = regexp.MustCompile(`^!![a-z]+$`)
)

// readableYAML is a YAML error as Klyx passes it on.
func readableYAML(err error) error {
	if err == nil {
		return nil
	}
	var te *yaml.TypeError
	if errors.As(err, &te) {
		issues := make([]string, len(te.Errors))
		for i, e := range te.Errors {
			issues[i] = readableIssue(e)
		}
		return errors.New("yaml: unmarshal errors:\n  " + strings.Join(issues, "\n  "))
	}
	body, ok := strings.CutPrefix(err.Error(), "yaml: ")
	if !ok {
		return errors.New(withheldYAML)
	}
	line := ""
	if m := yamlAtLine.FindStringSubmatch(body); m != nil {
		line, body = m[1], m[2]
	}
	at := func(s string) error {
		if line != "" {
			return fmt.Errorf("yaml: line %s: %s", line, s)
		}
		return errors.New("yaml: " + s)
	}
	switch {
	case yamlProblems[body], yamlDecoderFixed[body], yamlDynamicFixed.MatchString(body):
		return err
	case strings.HasPrefix(body, "unknown anchor "):
		return at("an alias names an anchor the file does not define")
	case strings.HasPrefix(body, "anchor ") && strings.HasSuffix(body, " value contains itself"):
		return at("an anchor's value contains itself")
	case strings.HasPrefix(body, "invalid map key:"):
		return at("a key is a list or a map, which a fleet file cannot use")
	}
	if m := yamlResolve.FindStringSubmatch(body); m != nil {
		if yamlTag.MatchString(m[2]) {
			return at("a value tagged " + m[2] + " is not one (its value is withheld)")
		}
		return at("a tagged value its tag cannot hold (its value is withheld)")
	}
	return at(withheldYAML)
}

const withheldYAML = "the YAML here cannot be read; Klyx withholds this error's text, which may hold the file's content"

var (
	issueLine    = regexp.MustCompile(`^line (\d+): `)
	issueDupKey  = regexp.MustCompile(`^line (\d+): mapping key ("(?:[^"\\]|\\.)*") already defined at line (\d+)$`)
	issueUnmarsh = regexp.MustCompile(`^line (\d+): cannot unmarshal (\S+)( .*)? into (.+)$`)
)

// standardTags are YAML's own tags: a type error naming one names nothing
// of the file's beyond a value yaml.v3 already cut to seven characters.
var standardTags = map[string]bool{"!!str": true, "!!int": true, "!!bool": true, "!!float": true, "!!null": true,
	"!!map": true, "!!seq": true, "!!timestamp": true, "!!binary": true, "!!merge": true}

// readableIssue is one type error's issue as Klyx passes it on: a key given
// twice by describeKey, a type error with a standard tag as yaml.v3 gave
// it, anything else as its line alone.
func readableIssue(e string) string {
	if m := issueDupKey.FindStringSubmatch(e); m != nil {
		k, err := strconv.Unquote(m[2])
		if err != nil {
			k = m[2]
		}
		return fmt.Sprintf("line %s: mapping key %s already defined at line %s", m[1], describeKey(k), m[3])
	}
	if m := issueUnmarsh.FindStringSubmatch(e); m != nil {
		if standardTags[m[2]] {
			return e
		}
		return fmt.Sprintf("line %s: cannot unmarshal a value with a custom tag into %s", m[1], m[4])
	}
	if m := issueLine.FindStringSubmatch(e); m != nil {
		return fmt.Sprintf("line %s: a value Klyx cannot read", m[1])
	}
	return "a value Klyx cannot read"
}

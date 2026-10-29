package promqlproof

import (
	"fmt"
	"sort"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

// Check parses q and requires every vector selector, a range selector's
// included, to carry every scope pair as an equality matcher with its exact
// decoded value, at least one selector to exist, and no operand to make a
// series from nothing (vector()).
func Check(q string, scope map[string]string) error {
	expr, err := parser.NewParser(parser.Options{}).ParseExpr(q)
	if err != nil {
		return fmt.Errorf("does not parse: %w", err)
	}
	keys := make([]string, 0, len(scope))
	for k := range scope {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	selectors := 0
	var bad error
	parser.Inspect(expr, func(n parser.Node, _ []parser.Node) error {
		if bad != nil {
			return bad
		}
		switch n := n.(type) {
		case *parser.Call:
			if n.Func.Name == "vector" {
				bad = fmt.Errorf("%s makes a series from no selector", n)
			}
		case *parser.VectorSelector:
			selectors++
			for _, k := range keys {
				if !has(n.LabelMatchers, k, scope[k]) {
					bad = fmt.Errorf("selector %s lacks %s=%q", n, k, scope[k])
					break
				}
			}
		}
		return bad
	})
	if bad != nil {
		return bad
	}
	if selectors == 0 {
		return fmt.Errorf("no vector selector: the query reads no series")
	}
	return nil
}

// has: an equality matcher on name with exactly value.
func has(ms []*labels.Matcher, name, value string) bool {
	for _, m := range ms {
		if m.Type == labels.MatchEqual && m.Name == name && m.Value == value {
			return true
		}
	}
	return false
}

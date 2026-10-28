package metrics

// raw is a test's query of literal text: the parsers' tests read responses,
// not templates. Production queries are registered templates only.
func raw(promql string) Query {
	return Query{name: "test", render: func(string) string { return promql }}
}

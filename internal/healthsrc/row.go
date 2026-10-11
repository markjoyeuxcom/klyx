package healthsrc

import "github.com/moomora/klyx/internal/health"

// The evaluated row envelope (1c slice 3 §2a). A part's rows are not bare
// domain objects: each row carries its identity and the evaluation from the
// SAME capture that produced the part's counts. The lens DTO converters copy
// that evaluation; they never evaluate a rule, read the clock for one, or
// fetch the current object - so a boundary crossed between evaluation and
// conversion cannot change a row's verdict without changing its part's Ver.

// RowRef is a row's identity: UID, the served GVR it was read at, and its
// generation where the kind has one.
type RowRef struct {
	UID, GVR, Namespace, Name string
	Generation                int64
}

// Row is one evaluated row. Eval is the native evaluation (relocated lens
// rules); verdict parts carry their verdict inside Data, and Eval is zero.
type Row[T any] struct {
	Ref  RowRef
	Eval health.NativeEval
	Data T
}

// RowSet is a part's rows with the facts the evaluation used (for
// workloads, fluxPresent), so a converter never reads a later one.
type RowSet[T any] struct {
	Rows  []Row[T]
	Facts map[string]any
}

func (r refInput) rowRef() RowRef {
	return RowRef{UID: r.uid, GVR: r.gvr, Namespace: r.ns, Name: r.name, Generation: r.generation}
}

// nativeRow wraps one native item and its data.
func nativeRow[T any](it nativeItem, data T) Row[T] {
	return Row[T]{Ref: it.ref.rowRef(), Eval: it.eval, Data: data}
}

func rowSet[T any](rows []Row[T]) RowSet[T] { return RowSet[T]{Rows: rows} }

// verdictRows wraps a verdict part's rows: the verdict is in the data.
func verdictRows[T any](data []T, refs []refInput) []Row[T] {
	out := make([]Row[T], len(data))
	for i := range data {
		out[i] = Row[T]{Ref: refs[i].rowRef(), Data: data[i]}
	}
	return out
}

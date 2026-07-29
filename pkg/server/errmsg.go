package server

// EmptyErrorMessage is what ErrorMessage substitutes for an error whose
// Error() is the empty string. It names the CONTRACT that was broken (an error
// must describe itself) rather than guessing at a cause, because there is
// nothing to guess from: the value reached us with no information at all.
const EmptyErrorMessage = "handler returned an error with an empty message"

// ErrorMessage renders err for the `error` field of a response / async result.
//
// It exists because an EMPTY message is not the same thing as "no error" on this
// wire, and every consumer downstream had to guess which one it was looking at.
// The generated server writes result and error in an exclusive if/else, so an
// errored response carries the error field and NO result field — but
// `b.CreateString("")` still returns a non-zero offset, so `errors.New("")` (or
// any custom error type with an empty Error(), e.g. `&MyErr{}` before its
// fields are set) produced a PRESENT error field of size 0.
//
// The C++ consumers all keyed off `error() && error()->size() > 0`, so that
// response was routed to the RESULT path instead:
//
//   - `string` return: `resp->result()->str()` on an absent field dereferenced
//     nullptr and killed the Excel process (the generated sync UDF runs outside
//     XLL_SAFE_BLOCK, so the access violation was not contained). Unsaved
//     workbooks were lost.
//   - `int`/`float`/`bool`: the absent field read back as the FlatBuffers
//     default, so the cell showed 0 / 0.0 / FALSE with NO error — a silent
//     wrong answer, which for a financial add-in is worse than the crash.
//   - `any`: an empty cell. Only `grid` was defended, by
//     GridToXLOPER12(nullptr) rendering #VALUE!.
//
// The C++ guard is now presence-only, which removes the crash. Normalizing here
// is the other half: it is what makes the cell show something DIAGNOSABLE
// instead of an empty string, and it keeps a newer server safe in front of an
// XLL built before that guard changed. Fixing only one side leaves a real
// failure mode in the field, so both landed together.
//
// A nil error returns "" — callers must only reach this on the error path.
func ErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if msg := err.Error(); msg != "" {
		return msg
	}
	return EmptyErrorMessage
}

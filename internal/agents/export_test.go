package agents

// SetLookPath swaps the PATH resolver for a test that has to answer for a
// machine it does not have, and returns a func that puts the real one back.
func SetLookPath(fn func(string) (string, error)) func() {
	prev := lookPath
	lookPath = fn
	return func() { lookPath = prev }
}

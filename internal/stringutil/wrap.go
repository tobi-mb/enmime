package stringutil

// Wrap builds a byte slice from strs, wrapping on word boundaries before max chars
func Wrap(max int, indentChar byte, wrapWithLFOnly bool, strs ...string) []byte {
	input := make([]byte, 0)
	output := make([]byte, 0)
	for _, s := range strs {
		input = append(input, []byte(s)...)
	}
	if len(input) < max {
		// Doesn't need to be wrapped
		return input
	}
	ls := -1 // Last seen space index
	lw := -1 // Last written byte index
	ll := 0  // Length of current line
	for i := 0; i < len(input); i++ {
		ll++
		switch input[i] {
		case ' ', '\t':
			ls = i
		}
		if ll >= max {
			if ls >= 0 {
				output = append(output, input[lw+1:ls]...)
				if wrapWithLFOnly {
					output = append(output, '\n', indentChar)
				} else {
					output = append(output, '\r', '\n', indentChar)
				}
				lw = ls // Jump over the space we broke on
				ll = 1  // Count leading space above
				// Rewind
				i = lw + 1
				ls = -1
			}
		}
	}
	return append(output, input[lw+1:]...)
}

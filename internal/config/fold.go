package config

// fold maps the letters NFKD decomposes into an ASCII base letter plus
// combining marks (Latin-1 Supplement, Latin Extended-A, fullwidth ASCII) to
// that base — what `name.normalize('NFKD').replace(/[̀-ͯ]/g, ”)`
// leaves behind. Letters without a decomposition (ß, æ, ø, ł, đ …) are not
// listed: they become "-" like any other non-[a-z0-9] character.
func fold(r rune) (string, bool) {
	if r >= 0xFF01 && r <= 0xFF5E { // fullwidth ASCII
		return string(r - 0xFEE0), true
	}
	s, ok := foldTable[r]
	return s, ok
}

var foldTable = map[rune]string{}

func init() {
	// Latin-1 Supplement
	add := func(base string, runes string) {
		for _, r := range runes {
			foldTable[r] = base
		}
	}
	add("A", "ÀÁÂÃÄÅ")
	add("C", "Ç")
	add("E", "ÈÉÊË")
	add("I", "ÌÍÎÏ")
	add("N", "Ñ")
	add("O", "ÒÓÔÕÖ")
	add("U", "ÙÚÛÜ")
	add("Y", "Ý")
	add("a", "àáâãäå")
	add("c", "ç")
	add("e", "èéêë")
	add("i", "ìíîï")
	add("n", "ñ")
	add("o", "òóôõö")
	add("u", "ùúûü")
	add("y", "ýÿ")
	// Latin Extended-A, U+0100–U+017F, in code point order; "" = no decomposition.
	extA := []string{
		"A", "a", "A", "a", "A", "a", "C", "c", "C", "c", "C", "c", "C", "c", "D", "d",
		"", "", "E", "e", "E", "e", "E", "e", "E", "e", "E", "e", "G", "g", "G", "g",
		"G", "g", "G", "g", "H", "h", "", "", "I", "i", "I", "i", "I", "i", "I", "i",
		"I", "", "IJ", "ij", "J", "j", "K", "k", "", "L", "l", "L", "l", "L", "l", "L·",
		"l·", "", "", "N", "n", "N", "n", "N", "n", "ʼn", "", "", "O", "o", "O", "o",
		"O", "o", "", "", "R", "r", "R", "r", "R", "r", "S", "s", "S", "s", "S", "s",
		"S", "s", "T", "t", "T", "t", "", "", "U", "u", "U", "u", "U", "u", "U", "u",
		"U", "u", "U", "u", "W", "w", "Y", "y", "Y", "Z", "z", "Z", "z", "Z", "z", "s",
	}
	for i, s := range extA {
		if s != "" {
			foldTable[rune(0x100+i)] = s
		}
	}
}

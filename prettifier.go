package gotestdox

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// Prettify takes a string input representing the name of a Go test, and
// attempts to turn it into a readable sentence, by replacing camel-case
// transitions and underscores with spaces.
//
// input is expected to be a valid Go test name, as produced by 'go test
// -json'. For example, input might be the string:
//
//	TestFoo/has_well-formed_output
//
// Here, the parent test is TestFoo, and this data is about a subtest whose
// name is 'has well-formed output'. Go's [testing] package replaces spaces in
// subtest names with underscores, and unprintable characters with the
// equivalent Go literal.
//
// Prettify does its best to reverse this transformation, yielding (something
// close to) the original subtest name. For example:
//
//	Foo has well-formed output
//
// # Multiword function names
//
// Because Go function names are often in camel-case, there's an ambiguity in
// parsing a test name like this:
//
//	TestHandleInputClosesInputAfterReading
//
// We can see that this is about a function named HandleInput, but Prettify has
// no way of knowing that. Without this information, it would produce:
//
//	Handle input closes input after reading
//
// To give it a hint, we can add an underscore after the name of the function:
//
//	TestHandleInput_ClosesInputAfterReading
//
// This will be interpreted as marking the end of a multiword function name:
//
//	HandleInput closes input after reading
//
// # Debugging
//
// If the GOTESTDOX_DEBUG environment variable is set, Prettify will output
// (copious) debug information to the [DebugWriter] stream, elaborating on its
// decisions.
func Prettify(input string) string {
	p := &prettifier{
		input: []rune(strings.TrimPrefix(input, "Test")),
		words: []string{},
		debug: io.Discard,
	}
	if os.Getenv("GOTESTDOX_DEBUG") != "" {
		p.debug = DebugWriter
	}
	p.log("input:", input)
	for state := betweenWords; state != nil; {
		state = state(p)
	}
	result := strings.Join(p.words, " ")
	p.log(fmt.Sprintf("result: %q", result))
	return result
}

// Heavily inspired by Rob Pike's talk on 'Lexical Scanning in Go':
// https://www.youtube.com/watch?v=HxaD_trXwRE
type prettifier struct {
	debug          io.Writer
	input          []rune
	start, pos     int
	words          []string
	inSubTest      bool
	seenUnderscore bool
}

func (p *prettifier) skip() {
	p.start = p.pos
}

func (p *prettifier) prev() rune {
	return p.input[p.pos-1]
}

func (p *prettifier) walk() rune {
	next := p.peek()
	p.pos++
	return next
}

func (p *prettifier) next() rune {
	// calculate next to prevent out of bounds
	// when word ends with capitalisation
	if p.pos+1 >= len(p.input) {
		return p.input[p.pos]
	}
	return p.input[p.pos+1]
}

func (p *prettifier) peek() rune {
	if p.pos >= len(p.input) {
		return eof
	}
	next := p.input[p.pos]
	return next
}

func (p *prettifier) isInitialism() bool {
	// calulate end to avoid last index to be
	// bigger than first index
	end := p.pos - 1
	if end < p.start {
		end = p.start
	}
	for _, r := range p.input[p.start:end] {
		if unicode.IsLower(r) {
			return false
		}
	}
	return unicode.IsUpper(p.input[end]) || unicode.IsDigit(p.input[end]) || p.input[end] == 's'
}

func (p *prettifier) inInitialism() bool {
	// capitalisation
	if unicode.IsUpper(p.prev()) && unicode.IsUpper(p.next()) {
		return true
	}
	// capitalisation with digit
	if unicode.IsUpper(p.prev()) && unicode.IsDigit(p.next()) {
		return true
	}
	// capitalisation ending in plural
	if unicode.IsUpper(p.prev()) && p.next() == 's' {
		return true
	}
	return false
}

func (p *prettifier) emit() {
	word := string(p.input[p.start:p.pos])
	switch {
	case len(p.words) == 0:
		// this is the first word
		word = cases.Title(language.Und, cases.NoLower).String(word)
	case len(word) < 3:
		// single and double letter word such as A or Is but not OK
		if word == "OK" {
			break
		}
		word = cases.Lower(language.Und).String(word)
	case p.isInitialism():
		// leave capitalisation as is
	default:
		word = cases.Lower(language.Und).String(word)
	}
	p.log(fmt.Sprintf("emit %q", word))
	p.words = append(p.words, word)
	p.skip()
}

func (p *prettifier) multiWordFunction() {
	var fname string
	for _, w := range p.words {
		fname += cases.Title(language.Und, cases.NoLower).String(w)
	}
	p.log("multiword function", fname)
	p.words = []string{fname}
	p.seenUnderscore = true
}

func (p *prettifier) log(args ...interface{}) {
	fmt.Fprintln(p.debug, args...)
}

func (p *prettifier) logState(stateName string) {
	next := "EOF"
	if p.pos < len(p.input) {
		next = string(p.input[p.pos])
	}
	p.log(fmt.Sprintf("%s: [%s] -> %s",
		stateName,
		string(p.input[p.start:p.pos]),
		next,
	))
}

type stateFunc func(p *prettifier) stateFunc

func betweenWords(p *prettifier) stateFunc {
	for {
		p.logState("betweenWords")
		switch p.walk() {
		case eof:
			return nil
		case '_', '/':
			p.skip()
		default:
			return inWord
		}
	}
}

func inWord(p *prettifier) stateFunc {
	for {
		p.logState("inWord")
		switch r := p.peek(); {
		case r == eof:
			p.emit()
			return nil
		case r == '_':
			p.emit()
			if !p.seenUnderscore && !p.inSubTest {
				// special 'end of function name' marker
				p.multiWordFunction()
			}
			return betweenWords
		case r == '/':
			p.emit()
			p.inSubTest = true
			return betweenWords
		case unicode.IsDigit(r):
			if unicode.IsDigit(p.prev()) {
				// in a multi-digit number
				p.walk()
				continue
			}
			if p.prev() == '-' {
				// in a negative number
				p.walk()
				continue
			}
			if p.prev() == '=' {
				// in some phrase like 'n=3'
				p.walk()
				continue
			}
			if p.inInitialism() {
				p.walk()
				continue
			}
			p.emit()
			return betweenWords
		case unicode.IsUpper(r):
			if p.inInitialism() {
				p.walk()
				continue
			}
			p.emit()
			return betweenWords
		default:
			p.walk()
		}
	}
}

const eof rune = 0

// DebugWriter identifies the stream to which debug information should be
// printed, if desired. By default it is [os.Stderr].
var DebugWriter io.Writer = os.Stderr

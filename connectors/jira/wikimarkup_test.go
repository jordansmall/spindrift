package main

import "testing"

func TestToWikiMarkup(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"empty", "", ""},
		{"headings", "# a\n###### f\n####### seven", "h1. a\nh6. f\n####### seven"},
		{"bold", "**a** and __b__", "*a* and *b*"},
		{"italic star", "*a* b", "_a_ b"},
		{"italic underscore unchanged", "_a_ b", "_a_ b"},
		{"snake case unchanged", "use foo_bar_baz here", "use foo_bar_baz here"},
		{"snake case dunder unchanged", "a__b__c and x_ y_", "a__b__c and x_ y_"},
		{"arithmetic stars unchanged", "2*3*4 and a * b * c", "2*3*4 and a * b * c"},
		{"inline code", "run `go test` now", "run {{go test}} now"},
		{"code shields emphasis", "`**a** *b*`", `{{\*\*a\*\* \*b\*}}`},
		{"code span escapes underscore", "`_x_`", `{{\_x\_}}`},
		{"code span escapes dash", "`-x-`", `{{\-x\-}}`},
		{"code span escapes other markup", "`+a^b~c?d!e|f`", `{{\+a\^b\~c\?d\!e\|f}}`},
		{"code span trailing backslash", "`a\\`", `{{a&#92;}}`},
		{"link", "[t](https://x.io/a_b_c)", "[t|https://x.io/a_b_c]"},
		{"link text pipe", "[a|b](https://x.io)", `[a\|b|https://x.io]`},
		{"link text code span pipe", "[`a|b`](https://x.io)", `[{{a\|b}}|https://x.io]`},
		{"image left alone", "![alt](https://x.io/p.png)", "![alt](https://x.io/p.png)"},
		{"bare url shielded", "go https://x.io/a_b*c*d/_e_ now", "go https://x.io/a_b*c*d/_e_ now"},
		{"bullets nest", "- a\n  - b\n    - c\n- d", "* a\n** b\n*** c\n* d"},
		{"four space nesting", "- a\n    - b\n- c", "* a\n** b\n* c"},
		{"ordered", "1. a\n2. b", "# a\n# b"},
		{"mixed nesting", "1. a\n   - b\n     1. c", "# a\n#* b\n#*# c"},
		{"star bullet is not italic", "* a\n* b", "* a\n* b"},
		{"hr", "---\n***\n___\n- - -", "----\n----\n----\n----"},
		{"quote single", "> hi **x**", "bq. hi *x*"},
		{"quote multi", "> a\n> b", "{quote}\na\nb\n{quote}"},
		{"fence lang", "```go\nx := **1**\n```", "{code:go}\nx := **1**\n{code}"},
		{"fence bare", "```\n# no\n```", "{code}\n# no\n{code}"},
		{"fence unterminated verbatim", "```go\nx", "```go\nx"},
		{"fence unterminated converts following lines", "```go\n**b**", "```go\n*b*"},
		{"fence four backticks holds inner fence", "````\n```\ninner\n```\n````", "{code}\n```\ninner\n```\n{code}"},
		{"fence closer may be longer", "```\nx\n`````", "{code}\nx\n{code}"},
		{"fence closer carries no info string", "```\nx\n```go\ny\n```", "{code}\nx\n```go\ny\n{code}"},
		{"fence bad language dropped", "```go}\nx\n```", "{code}\nx\n{code}"},
		{"fence language charset kept", "```c++\nx\n```", "{code:c++}\nx\n{code}"},
		{"fence body closing code uses noformat", "```go\na\n{code}\nb\n```", "{noformat}\na\n{code}\nb\n{noformat}"},
		{"emphasis closer skips code span", "**a `b**` c**", `*a {{b\*\*}} c*`},
		{"emphasis closer skips escape", `**a \** b**`, `*a \** b*`},
		{"lazy continuation keeps nesting", "- a\ncontinued\n  - b", "* a\ncontinued\n** b"},
		{"lazy continuation chains", "- a\nc1\nc2\n  - b", "* a\nc1\nc2\n** b"},
		{"blank ends lazy continuation", "- a\n\ncontinued\n  - b", "* a\n\ncontinued\n* b"},
		{"heading resets list nesting", "- a\n# h\n  - b", "* a\nh1. h\n* b"},
		{"table", "|a|b|\n|---|---|\n|**1**||", "||a||b||\n|*1*| |"},
		{"table pipe in code span", "| a | `x|y` |\n|---|---|\n| 1 | `p|q` |", `||a||{{x\|y}}||` + "\n" + `|1|{{p\|q}}|`},
		{"table escaped pipe", "| a | b\\|c |\n|---|---|", "||a||b\\|c||"},
		{"table escaped pipe in code span", "| a | `x\\|y` |\n|---|---|", `||a||{{x\|y}}||`},
		{"table escaped pipe beside code span", "| a | `p` b\\|c |\n|---|---|", `||a||{{p}} b\|c||`},
		{"table unclosed backtick splits", "| a | `b |\n|---|---|", "||a||`b||"},
		{"pipes without separator unchanged", "| a | b |", "| a | b |"},
		{"trailing newline kept", "a\n", "a\n"},
		{"link text keeps escaped brackets", `- [Handle \[skip ci\] commits](https://x.io/1)`, `* [Handle \[skip ci\] commits|https://x.io/1]`},
		{"link text pipe escaped", `[A | B](https://x.io/2)`, `[A \| B|https://x.io/2]`},
		{"link text escaped pipe not doubled", `[A \| B](https://x.io/2)`, `[A \| B|https://x.io/2]`},
		{"escaped backslash collapses", `[a\\b](u)`, `[a\b|u]`},
		{"escaped brackets inside bold", `**x \[y\]**`, `*x \[y\]*`},
		{"escaped stars stay literal", `\*not italic\*`, `\*not italic\*`},
		{"escaped bracket opens no link", `\[t](https://x.io/a)`, `\[t](https://x.io/a)`},
		{"code span backslashes are entities", "`a\\\\b`", `{{a&#92;&#92;b}}`},
		{"code span escapes braces", "`struct{}`", `{{struct\{\}}}`},
		{"code span escapes brackets and braces", "`map[string]struct{}`", `{{map\[string\]struct\{\}}}`},
		{"code span escapes template braces", "`{{ .Field }}`", `{{\{\{ .Field \}\}}}`},
		{"code span escapes macro", "`{code}`", `{{\{code\}}}`},
		{"code span backslash is literal before brace", "`\\{`", `{{&#92;\{}}`},
		{"table code span braces", "| a | `struct{}` |\n|---|---|", `||a||{{struct\{\}}}||`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := toWikiMarkup(tc.in); got != tc.want {
				t.Errorf("toWikiMarkup(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// Marker lines and syntax the converter does not model must reach Jira
// byte-identical, as must a body with no markup at all.
func TestToWikiMarkup_PassesThroughByteIdentical(t *testing.T) {
	for _, in := range []string{
		"plain sentence with no markup.",
		"<!-- spindrift-research -->",
		"<!-- spindrift-dedup: abc_def **x** [a](b) -->",
		"> <!-- spindrift-research -->",
		"- <!-- spindrift-x --> *y*",
		"text\n<!-- spindrift-butler: `k` -->\nmore",
		"<div>raw html</div>",
		"~~~\ntilde fence\n~~~",
		"***bold italic***",
		"a\r\nb\r\n",
	} {
		if got := toWikiMarkup(in); got != in {
			t.Errorf("toWikiMarkup(%q) = %q, want unchanged", in, got)
		}
	}
}

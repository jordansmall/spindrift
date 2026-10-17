package driverkit

import (
	"path/filepath"
	"testing"
)

func TestResultText(t *testing.T) {
	results := func(texts ...string) func() Decoder {
		return func() Decoder {
			return func(line string) []Event {
				switch line {
				case "r1":
					return []Event{{Kind: KindText, Text: "prose"}, {Kind: KindResult, Text: texts[0]}}
				case "r2":
					return []Event{{Kind: KindResult, Text: texts[1]}}
				}
				return nil
			}
		}
	}
	t.Run("missing log is empty", func(t *testing.T) {
		got, err := ResultText(filepath.Join(t.TempDir(), "absent.log"), results("a", "b"))
		if err != nil || got != "" {
			t.Errorf("ResultText(missing) = %q, %v; want \"\", nil", got, err)
		}
	})
	t.Run("every result, one per line, non-result events and empty values dropped", func(t *testing.T) {
		path := writeClassifyLog(t, "r1", "noise", "r2", "r1")
		got, err := ResultText(path, results("one", "two"))
		if err != nil {
			t.Fatal(err)
		}
		if want := "one\ntwo\none\n"; got != want {
			t.Errorf("ResultText = %q, want %q", got, want)
		}
		path = writeClassifyLog(t, "r1", "r2")
		if got, _ := ResultText(path, results("", "x")); got != "x\n" {
			t.Errorf("ResultText with empty result = %q, want %q", got, "x\n")
		}
	})
}

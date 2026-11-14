package driverkit

import (
	"reflect"
	"strings"
	"testing"
)

func TestStrategyBatchDecoderFallsBackToOneDecoderPerLine(t *testing.T) {
	builds := 0
	s := Strategy{NewDecoder: func() Decoder {
		builds++
		n := 0
		return func(line string) []Event {
			n++
			return []Event{{Kind: KindText, Text: strings.TrimSpace(line) + string(rune('0'+n))}}
		}
	}}
	b := s.BatchDecoder()
	got := append(b([]string{"a", "b"}), b([]string{"c"})...)
	want := [][]Event{
		{{Kind: KindText, Text: "a1"}},
		{{Kind: KindText, Text: "b2"}},
		{{Kind: KindText, Text: "c3"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if builds != 1 {
		t.Errorf("built %d decoders, want one shared across batches", builds)
	}
}

func TestStrategyBatchDecoderPrefersItsOwn(t *testing.T) {
	s := Strategy{
		NewDecoder:      func() Decoder { t.Error("NewDecoder used"); return nil },
		NewBatchDecoder: func() BatchDecoder { return func(l []string) [][]Event { return make([][]Event, len(l)+1) } },
	}
	if got := s.BatchDecoder()([]string{"x"}); len(got) != 2 {
		t.Errorf("got %d results, want the strategy's own batch decoder's 2", len(got))
	}
}

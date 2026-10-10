package searchtext

import "testing"

func TestIndexedKeepsTextAndAddsHanNgrams(t *testing.T) {
	got := Indexed("信道估计 OFDM 导频")
	want := "信道估计 OFDM 导频 信 信道 道 道估 估 估计 计 导 导频 频"
	if got != want {
		t.Fatalf("got %q", got)
	}
	if got := Indexed("plain text"); got != "plain text" {
		t.Fatalf("non-Han text changed: %q", got)
	}
}

func TestQueryMatchesAnyTerm(t *testing.T) {
	cases := []struct {
		in, want, bigrams string
		han               bool
	}{
		{"信道估计", `"信道" OR "道估" OR "估计"`, `"信道" OR "道估" OR "估计"`, true},
		{"信", `"信"`, ``, true},
		{"信道估计 pilot", `"信道" OR "道估" OR "估计" OR "pilot"`, `"信道" OR "道估" OR "估计"`, true},
		{"OFDM信道", `"OFDM" OR "信道"`, `"信道"`, true},
		{"LoS 和 NLoS", `"LoS" OR "和" OR "NLoS"`, ``, true},
		{"如何估计？", `"如何" OR "何估" OR "估计"`, `"如何" OR "何估" OR "估计"`, true},
		{"channel estimation", `"channel" OR "estimation"`, ``, false},
		{"", ``, ``, false},
	}
	for _, c := range cases {
		got, bigrams, han := Query(c.in)
		if got != c.want || bigrams != c.bigrams || han != c.han {
			t.Fatalf("%q: got %q %q %v, want %q %q %v", c.in, got, bigrams, han, c.want, c.bigrams, c.han)
		}
	}
}

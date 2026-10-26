package theme

import "testing"

func TestFitDropsLineBreaks(t *testing.T) {
	if got := Fit("a\nb\tc\r\nd", 9); got != "a b c d  " {
		t.Errorf("Fit = %q", got)
	}
	if got := OneLine(" Street 8 \n\n Moscow \n"); got != "Street 8, Moscow" {
		t.Errorf("OneLine = %q", got)
	}
}

package main

import (
	"bytes"
	"testing"
	"time"
)

func answerSoon(yes bool) {
	go func() {
		for i := 0; i < 50; i++ {
			time.Sleep(20 * time.Millisecond)
			mcMu.Lock()
			asking := mcCur.Ask != ""
			mcMu.Unlock()
			if asking {
				mcAnswerCh <- yes
				return
			}
		}
	}()
}

// copying a save that's already on the card asks first; no keeps the card, yes overwrites
func TestMcAddSaveAsks(t *testing.T) {
	card, _ := mcImport(mcBlank(), testSave("BASLUS-00067DRACULA", 2))
	same := testSave("BASLUS-00067DRACULA", 2)
	answerSoon(false)
	out, force, err := mcAddSave(card, same)
	if err != nil || !bytes.Equal(out, card) || len(force) != 0 {
		t.Fatalf("no: card changed (%v)", err)
	}
	if mcCur.AskID != 1 || !bytes.Contains([]byte(mcCur.Log[len(mcCur.Log)-1]), []byte("Left")) {
		t.Fatalf("no question asked: %v", mcCur.Log)
	}
	answerSoon(true)
	out, force, err = mcAddSave(card, same)
	if err != nil || !bytes.Equal(out, card) { // identical save, same blocks: same card
		t.Fatalf("yes, identical: %v", err)
	}
	// ...but its 2 blocks are written anyway: 2 directory frames + 2 x 64 sectors
	if len(force) != 130 || force[0] != 1 || force[1] != 64 {
		t.Fatalf("forced sectors: %d %v", len(force), force[:3])
	}
	other := testSave("BASLUS-00067DRACULA", 1) // a different save with the same name
	other[mcFrame+100] = 7
	answerSoon(true)
	out, _, err = mcAddSave(card, other)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := mcParse(out, false)
	if len(c.Saves) != 1 || len(c.Saves[0].Blocks) != 1 || c.Free != 14 {
		t.Fatalf("replaced card: %+v", c)
	}
	if mcCur.AskID != 3 || !bytes.Contains([]byte(mcCur.Log[len(mcCur.Log)-2]), []byte("different save")) {
		t.Fatalf("question for a different save: %v", mcCur.Log)
	}
	// a new save goes on without asking
	_, force, err = mcAddSave(card, testSave("BASCUS-94163FF7-S01", 1))
	if err != nil || mcCur.AskID != 3 || len(force) != 0 {
		t.Fatalf("asked for a new save (%v)", err)
	}
}

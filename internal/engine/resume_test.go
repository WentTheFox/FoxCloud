package engine

import (
	"testing"

	"github.com/WentTheFox/FoxCloud/internal/classify"
	"github.com/WentTheFox/FoxCloud/internal/state"
)

func TestResumeAfterFilterChangeForcesResync(t *testing.T) {
	e := &Engine{Store: state.Store{Dir: t.TempDir()}}
	for kind, wantResynced := range map[classify.Kind]bool{
		classify.KindFilterChange: false,
		classify.KindTooManyDel:   true,
	} {
		e.Store.Save("j", state.Job{Resynced: true, Paused: &state.Failure{Kind: string(kind)}})
		if err := e.Resume("j"); err != nil {
			t.Fatal(err)
		}
		st, _ := e.Store.Load("j")
		if st.Paused != nil || st.Resynced != wantResynced {
			t.Errorf("%s: paused=%v resynced=%v, want unpaused and resynced=%v", kind, st.Paused, st.Resynced, wantResynced)
		}
	}
}

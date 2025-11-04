package helpers

import (
	"os"
	"time"

	"github.com/briandowns/spinner"
)

type Spinner struct {
	sp *spinner.Spinner
}

func NewSpinner(title string) *Spinner {
	s := spinner.New(spinner.CharSets[11], 100*time.Millisecond, spinner.WithWriter(os.Stderr))
	s.Suffix = " " + title
	return &Spinner{
		sp: s,
	}
}

func (s *Spinner) Start() {
	s.sp.Start()
}

func (s *Spinner) Stop() {
	s.sp.Stop()
}

func (s *Spinner) Restart() {
	s.sp.Restart()
}

func (s *Spinner) Update(v string) {
	s.sp.Suffix = " " + v
}

func (s *Spinner) Final(v string) {
	s.sp.FinalMSG = v + "\n"
}

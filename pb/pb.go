package pb

import (
	"github.com/charmbracelet/log"
	"github.com/pocketbase/pocketbase"
)

func Run() {
	app := pocketbase.New()

	func() {
		if err := app.Start(); err != nil {
			log.Fatal(err)
		}
	}()

}
